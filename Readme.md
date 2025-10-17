# DummyClusterNetwork: CRD + controller for Multus bridge-backed networks (Harvester demo)

This directory contains a demo CustomResourceDefinition (DummyClusterNetwork), a controller, example manifests and a small helper image for creating a host bridge used by Multus NetworkAttachmentDefinitions. It's intended for Harvester clusters that run KubeVirt + Multus.

What it does
- Provide a cluster-scoped CRD: DummyClusterNetwork
- Controller ensures that when a DummyClusterNetwork is created:
  - A NetworkAttachmentDefinition (Multus) is created in a chosen namespace
  - A privileged DaemonSet exists which creates and maintains a host bridge on every node
- When the DummyClusterNetwork is deleted, the controller:
  - Refuses deletion (via a finalizer) until no VirtualMachines or VirtualMachineInstances reference the NAD
  - Once unused, deletes the DaemonSet and the NAD and removes the finalizer

Files added
- config/crd/crd.yaml: CRD for DummyClusterNetwork
- controllers/dummyclusternetwork_controller.go: controller implementation (controller-runtime, unstructured)
- main.go: small controller main
- go.mod: module file for controller (adjust module path as needed)
- deploy/controller/*: Deployment manifest + RBAC to run the controller
- images/daemon-bridge/: Dockerfile + script that repeatedly ensures the host bridge exists
- examples/: example NAD, DaemonSet, DummyClusterNetwork and example VM

Important security notes
- The DaemonSet runs privileged operations on the host. Replace the demo image with a built and vetted image for production.
- Adjust namespace choices (we used kube-system and harvester-system in examples) as appropriate.
- Ensure you have Multus and KubeVirt in the cluster.

How to build / deploy (summary)
1. Build the daemon-bridge image, push it to your registry:
   - docker build -t <registry>/daemon-bridge:tag images/daemon-bridge
   - docker push <registry>/daemon-bridge:tag
2. Build controller image, push to registry. Update deploy/controller/deployment.yaml replacing REPLACE_WITH_CONTROLLER_IMAGE.
3. Apply CRD:
   - kubectl apply -f config/crd/crd.yaml
4. Deploy RBAC and controller:
   - kubectl apply -f deploy/controller/role.yaml
   - kubectl apply -f deploy/controller/deployment.yaml
5. Create an example DummyClusterNetwork:
   - kubectl apply -f examples/dummyclusternetwork-example.yaml
6. Create VM which references the NAD in the example.

If you want, I can:
- Provide a patched deployment file with recommended resource requests/limits and liveness/readiness endpoints wired up.
- Generate a small Makefile for building the two images and pushing them to a registry.
- Create a small script to run simple integration checks (verify NAD exists, DaemonSet running, bridge exists on a node).

