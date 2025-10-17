# DummyClusterNetwork Controller — Design & Pseudocode (auto-create DCN)

This document captures the design, lifecycle, and pseudocode for a controller that
automatically bootstraps and manages a DummyClusterNetwork CR for Harvester
(KubeVirt + Multus). The controller:

- Auto-creates a cluster-scoped DummyClusterNetwork CR at startup (so users do not need to create it).
- Ensures a NetworkAttachmentDefinition (NAD) exists in a configured namespace.
- Ensures a privileged DaemonSet exists to create/maintain a host bridge used by Multus.
- Uses a finalizer on the DummyClusterNetwork CR to block deletion until no VirtualMachine or VirtualMachineInstance references the NAD.
- On safe teardown, deletes the DaemonSet and NAD and removes the finalizer.

Usage / intent
- This is intended as a clear specification / single-file design you can paste into your repo.
- Make the DCN parameters configurable (CLI flags or ConfigMap) in a real implementation.

--- 

## Configurable controller inputs
- controller-managed DCN name (e.g., `br-dummy-cluster`)
- bridgeName (e.g., `br-dummy`)
- nadName (e.g., `dummy-bridge-net`)
- nadNamespace (e.g., `harvester-system`)
- daemon image (e.g., `registry.example/daemon-bridge:suse-15.5`)
- controller image, RBAC, namespace for deployment

---

## High-level lifecycle (auto-create mode)
1. Controller starts.
2. Controller ensures a single authoritative DummyClusterNetwork CR exists:
   - If missing: create it with an annotation `network.harvester.io/managed-by=dummy-controller`.
3. Controller reconciles the DummyClusterNetwork:
   - Adds finalizer if missing.
   - Creates/ensures the NetworkAttachmentDefinition (NAD).
   - Creates/ensures a per-DCN DaemonSet that maintains the host bridge.
   - Updates CR status to `Ready`.
4. User VMs reference the NAD using Multus (no change to VM creation).
5. When CR deletion is requested, controller:
   - Checks all VirtualMachine and VirtualMachineInstance objects for references to NAD.
   - If any references exist: update status `DeletionBlocked` and requeue.
   - If none: delete DaemonSet, delete NAD, remove finalizer, allow CR deletion.

---

## Reconciler pseudocode (compact, for implementation reference)

Startup / main
```
function main():
    cfg = loadControllerConfig()  // CLI args or ConfigMap
    startController(cfg)
```

Controller startup: ensure DCN exists
```
function startController(cfg):
    ensureDefaultDummyClusterNetwork(cfg)
    register watches for:
      - DummyClusterNetwork
      - NetworkAttachmentDefinition (optional)
      - DaemonSet (optional)
      - VirtualMachine, VirtualMachineInstance (recommended)
    start control loop / manager
```

ensureDefaultDummyClusterNetwork
```
function ensureDefaultDummyClusterNetwork(cfg):
    dcn = GET DummyClusterNetwork cfg.dcnName
    if NotFound(dcn):
        newDcn = {
            apiVersion: "network.harvester.io/v1alpha1",
            kind: "DummyClusterNetwork",
            metadata: {
                name: cfg.dcnName,
                annotations: { "network.harvester.io/managed-by": "dummy-controller" },
            },
            spec: {
                bridgeName: cfg.bridgeName,
                nadName: cfg.nadName,
                nadNamespace: cfg.nadNamespace
            }
        }
        CREATE newDcn
    else if error:
        return error
    else:
        // Optionally reconcile spec drift if controller is authoritative
        if cfg.overwrite and dcn.spec != cfg.spec:
            dcn.spec = cfg.spec
            UPDATE dcn
```

Reconcile (called for the DCN and related resource changes)
```
function Reconcile(request):
    dcn = GET DummyClusterNetwork request.Name
    if NotFound(dcn):
        return

    bridge, foundB, errB = NestedString(dcn, "spec", "bridgeName")
    nadName, foundN, errN = NestedString(dcn, "spec", "nadName")
    nadNs, foundNs, errNs = NestedString(dcn, "spec", "nadNamespace")
    if errB or errN or errNs:
        log and requeue with error
    if not foundB or not foundN or not foundNs or any empty:
        updateStatus(dcn, "InvalidSpec", "bridgeName, nadName, nadNamespace required")
        requeue after 30s
        return

    if dcn.deletionTimestamp is zero:  // ensure/create path
        if finalizer not present:
            add finalizer; UPDATE dcn; // return to allow requeue/watch to observe change
            return

        err = ensureNetworkAttachmentDefinition(nadNs, nadName, bridge)
        if err:
            updateStatus(dcn, "ErrorEnsuringNAD", err.message)
            return error

        err = ensureBridgeDaemonSetForDCN(dcn.Name, bridge)
        if err:
            updateStatus(dcn, "ErrorEnsuringDaemonSet", err.message)
            return error

        updateStatus(dcn, "Ready", "NAD and DaemonSet present")
        return

    else: // deletion path
        fullName = nadNs + "/" + nadName
        inUse, users, err = isNetworkInUse(fullName, nadName)
        if err: return error
        if inUse:
            updateStatus(dcn, "DeletionBlocked", "In use by: " + join(users, ","))
            emit Event listing users
            requeue after 30s
            return

        err = deleteBridgeDaemonSetForDCN(dcn.Name)
        if err: return error
        err = deleteNetworkAttachmentDefinition(nadNs, nadName)
        if err: return error

        remove finalizer; UPDATE dcn
        updateStatus(dcn, "Deleted", "Resources removed; finalizer removed")
        return
```

Helper: ensureNetworkAttachmentDefinition
```
function ensureNetworkAttachmentDefinition(namespace, name, bridge):
    nad = GET NAD namespace/name
    if exists: return nil
    if other error: return error
    configJSON = {
       "cniVersion":"0.3.1",
       "type":"bridge",
       "bridge": bridge,
       "isGateway": true,
       "ipMasq": true,
       "ipam": { "type":"host-local", "subnet":"10.10.0.0/24" }
    }
    create NAD with spec.config = stringify(configJSON)
    return
```

Helper: ensureBridgeDaemonSetForDCN
```
function ensureBridgeDaemonSetForDCN(dcnName, bridge):
    dsName = "dummy-bridge-" + sanitize(dcnName)
    ds = GET DaemonSet kube-system/dsName
    if exists: return nil
    if other error: return error
    build DS spec with:
      - hostNetwork: true
      - hostPID: true
      - privileged container (daemon image)
      - env BRIDGE=bridge
    CREATE DS
    return
```

Helper: isNetworkInUse
```
function isNetworkInUse(fullName, shortName):
    users = []
    vmList = LIST VirtualMachine (all namespaces)
    for vm in vmList:
       if objectUsesNetwork(vm, fullName, shortName):
          users.append("VM/" + vm.ns + "/" + vm.name)

    vmiList = LIST VirtualMachineInstance (all namespaces)
    for vmi in vmiList:
       if objectUsesNetwork(vmi, fullName, shortName):
          users.append("VMI/" + vmi.ns + "/" + vmi.name)

    return len(users)>0, users, nil
```

Helper: objectUsesNetwork (checks common placements)
```
function objectUsesNetwork(obj, fullName, shortName):
    // 1) Check spec.template.spec.networks[*].multus.networkName
    // 2) Check spec.template.metadata.annotations["k8s.v1.cni.cncf.io/networks"] (comma-separated)
    // Compare entries to fullName, shortName, or suffix "/shortName"
    return bool
```

Deletion helpers
```
function deleteBridgeDaemonSetForDCN(dcnName):
    dsName = "dummy-bridge-" + sanitize(dcnName)
    ds = GET DaemonSet kube-system/dsName
    if NotFound: return nil
    DELETE ds
    // Optionally wait for pods to terminate
    return

function deleteNetworkAttachmentDefinition(namespace, name):
    nad = GET NAD namespace/name
    if NotFound: return nil
    DELETE nad
    return
```

---

## Some notes on what else can be done to improve this setup
- Check the (found, err) return values for NestedString and handle errors explicitly — do not ignore them.
- Use a per-DCN DaemonSet name (include sanitized DCN name) so multiple DCNs can coexist.
- Watch VirtualMachine and VirtualMachineInstance resources so deletion unblocks immediately when VMs are removed/edited; avoid repeated full-list polling.
- Consider an informer indexer to avoid listing all VMs/VMI on each check in large clusters.
- Emit Kubernetes Events to make it easy for operators to see which VMs block deletion.
- Use a vetted SUSE BCI (or SLES) base image for the daemon; require registry login if using SUSE's registry.
- Harden the DaemonSet image: minimal permissions, minimal packages, pinned version, and scan for CVEs.
- The controller should expose metrics and health/readiness endpoints.

---

## Example controller-managed CR annotation
Add `network.harvester.io/managed-by: dummy-controller` or similar to the auto-created CR so operators know the object is controller-owned and to avoid accidental manual edits.

---



