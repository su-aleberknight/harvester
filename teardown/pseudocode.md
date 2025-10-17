High-level flow

    User creates DummyClusterNetwork CR (spec: bridgeName, nadName, nadNamespace).
    Controller sees the new DCN:
        Adds a finalizer to block deletion.
        Creates a NetworkAttachmentDefinition (NAD) in nadNamespace configured to attach to the requested bridge.
        Creates a privileged DaemonSet that ensures the named bridge exists on each node.
        Marks DCN status Ready.
    User creates VirtualMachines that reference the NetworkAttachmentDefinition (via multus).
    User deletes the DummyClusterNetwork:
        Controller sees deletion request (object has deletionTimestamp).
        Controller checks all VirtualMachines and VirtualMachineInstances to see if any still reference the NAD.
        If any VMs/VMIs reference it: controller updates status to indicate deletion is blocked and requeues.
        If no VMs/VMIs reference it: controller deletes the DaemonSet and NAD, removes the finalizer, allowing the DCN to be deleted.

### Reconciler pseudocode (high level)
function Reconcile(request):
    dcn = GET DummyClusterNetwork request.Name
    if NotFound(dcn):
        return

    bridgeName, nadName, nadNamespace = extract spec fields from dcn
    if any field missing or invalid:
        updateStatus(dcn, "InvalidSpec", "bridgeName, nadName, nadNamespace required")
        requeue after short delay
        return

    if dcn.deletionTimestamp is zero:   // object is not being deleted -> ensure create/ensure state
        if finalizer not in dcn.finalizers:
            add finalizer to dcn
            UPDATE dcn

        // Ensure artifacts exist
        err = ensureNetworkAttachmentDefinition(nadNamespace, nadName, bridgeName)
        if err:
            updateStatus(dcn, "ErrorEnsuringNAD", err.message)
            return error (requeue)

        err = ensureBridgeDaemonSet(bridgeName)
        if err:
            updateStatus(dcn, "ErrorEnsuringDaemonSet", err.message)
            return error (requeue)

        updateStatus(dcn, "Ready", "Resources created")
        return

    else: // deletionTimestamp set -> teardown path
        fullNADName = nadNamespace + "/" + nadName

        inUse, users = isNetworkInUse(fullNADName, nadName)
        if inUse:
            updateStatus(dcn, "DeletionBlocked", "In use by: " + join(users))
            requeue after delay
            return

        // Safe to delete artifacts
        err = deleteBridgeDaemonSet(bridgeName)
        if err:
            return error (requeue)

        err = deleteNAD(nadNamespace, nadName)
        if err:
            return error (requeue)

        // Remove finalizer so the DCN object can be garbage collected
        remove finalizer from dcn
        UPDATE dcn
        updateStatus(dcn, "Deleted", "Cleaned up resources")
        return


### Helper functions pseudocode 

- ensureNetworkAttachmentDefinition(namespace, name, bridge)

function ensureNetworkAttachmentDefinition(namespace, name, bridge):
    nad = GET NetworkAttachmentDefinition namespace/name
    if nad exists:
        return nil
    if GET returned an error other than NotFound:
        return error

    configJSON = {
       "cniVersion":"0.3.1",
       "type":"bridge",
       "bridge": bridge,
       "isGateway": true,
       "ipMasq": true,
       "ipam": { ... default ipam config ... }
    }

    newNAD = Unstructured(
        apiVersion="k8s.cni.cncf.io/v1",
        kind="NetworkAttachmentDefinition",
        metadata: { name: name, namespace: namespace },
        spec: { config: serialize(configJSON) }
    )
    CREATE newNAD
    return nil or error

- ensureBridgeDaemonSet(bridge)

function ensureBridgeDaemonSet(bridge):
    dsName = sanitizedName("dummy-bridge-ensure-" + bridge)   // recommended: include bridge in name
    ds = GET DaemonSet kube-system/dsName
    if ds exists:
        return nil
    if GET returned an error other than NotFound:
        return error

    // Build a DaemonSet spec that runs a privileged container to create/maintain the host bridge
    newDS = DaemonSet(
       metadata: name=dsName, namespace="kube-system",
       spec: podTemplateSpec {
           hostNetwork: true
           hostPID: true
           tolerations: [Exists]
           containers: [
              {
                name: "ensure-bridge",
                image: DAEMON_IMAGE,
                securityContext.privileged = true,
                env: [ {name: "BRIDGE", value: bridge} ],
                // script ensures ip link add/type bridge and bring up interface in loop
              }
           ],
           volumes: hostPath /lib/modules readOnly
       }
    )
    CREATE newDS
    return nil or error

- isNetworkInUse(fullNADName, shortNADName)

function isNetworkInUse(fullNADName, shortNADName):
    users = []

    // List all VirtualMachines
    vmList = LIST VirtualMachine (all namespaces)
    for each vm in vmList:
        if objectUsesNetwork(vm, fullNADName, shortNADName):
            users.append("VM/" + vm.namespace + "/" + vm.name)

    // List all VirtualMachineInstances
    vmiList = LIST VirtualMachineInstance (all namespaces)
    for each vmi in vmiList:
        if objectUsesNetwork(vmi, fullNADName, shortNADName):
            users.append("VMI/" + vmi.namespace + "/" + vmi.name)

    return len(users) > 0, users, nil

- objectUsesNetwork(obj, fullName, shortName)

function objectUsesNetwork(obj, fullName, shortName):
    // 1) Check spec.template.spec.networks[*].multus.networkName
    networks = getNested(obj, ["spec","template","spec","networks"]) // returns list or nil
    if networks exists:
        for n in networks:
            if n has key "multus" and n.multus has "networkName":
                netName = n.multus.networkName
                if netName equals fullName OR netName equals shortName OR netName has suffix "/" + shortName:
                    return true

    // 2) Check Multus annotation in template: k8s.v1.cni.cncf.io/networks
    ann = getNestedString(obj, ["spec","template","metadata","annotations","k8s.v1.cni.cncf.io/networks"])
    if ann exists:
        // ann is comma-separated entries; compare each
        parts = split(ann, ",")
        for p in parts:
            t = trim(p)
            if t equals fullName OR t equals shortName OR t has suffix "/" + shortName:
                return true

    return false

- deleteBridgeDaemonSet(bridge)

function deleteBridgeDaemonSet(bridge):
    dsName = sanitizedName("dummy-bridge-ensure-" + bridge)
    ds = GET DaemonSet kube-system/dsName
    if NotFound(ds):
        return nil
    DELETE ds
    optionally: wait for DaemonSet pods to be deleted (or rely on Kubernetes)
    return nil or error

- deleteNAD(namespace, name)

function deleteNAD(namespace, name):
    nad = GET NetworkAttachmentDefinition namespace/name
    if NotFound(nad):
        return nil
    DELETE nad
    return nil or error

- updateStatus(obj, phase, reason)

function updateStatus(obj, phase, reason):
    obj.status = { phase: phase, reason: reason }   // or merge into existing status fields
    call Status().Update(obj)
    return nil or error


