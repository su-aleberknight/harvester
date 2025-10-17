package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	finalizerName = "finalizer.network.harvester.io/dummy"
)

var (
	dcnGroup   = "network.harvester.io"
	dcnVersion = "v1alpha1"
	dcnKind    = "DummyClusterNetwork"
)

type DummyClusterNetworkReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// RBAC annotations for controller generation / documentation.
// +kubebuilder:rbac:groups=network.harvester.io,resources=dummyclusternetworks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.harvester.io,resources=dummyclusternetworks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;create;update;delete;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;create;update;delete;patch;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines;virtualmachineinstances,verbs=get;list;watch

func (r *DummyClusterNetworkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": dcnGroup + "/" + dcnVersion, "kind": dcnKind}}).
		Complete(r)
}

func (r *DummyClusterNetworkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var dcn unstructured.Unstructured
	dcn.SetGroupVersionKind(schema.GroupVersionKind{Group: dcnGroup, Version: dcnVersion, Kind: dcnKind})
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &dcn); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	bridgeName, _, _ := unstructured.NestedString(dcn.Object, "spec", "bridgeName")
	nadName, _, _ := unstructured.NestedString(dcn.Object, "spec", "nadName")
	nadNamespace, _, _ := unstructured.NestedString(dcn.Object, "spec", "nadNamespace")
	if bridgeName == "" || nadName == "" || nadNamespace == "" {
		_ = r.updateStatus(ctx, &dcn, "InvalidSpec", "bridgeName, nadName and nadNamespace are required")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if dcn.GetDeletionTimestamp().IsZero() {
		if !containsString(dcn.GetFinalizers(), finalizerName) {
			dcn.SetFinalizers(append(dcn.GetFinalizers(), finalizerName))
			if err := r.Update(ctx, &dcn); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("added finalizer")
		}
		if err := r.ensureNetworkAttachmentDefinition(ctx, nadNamespace, nadName, bridgeName); err != nil {
			_ = r.updateStatus(ctx, &dcn, "ErrorEnsuringNAD", err.Error())
			return ctrl.Result{}, err
		}
		if err := r.ensureBridgeDaemonSet(ctx, bridgeName); err != nil {
			_ = r.updateStatus(ctx, &dcn, "ErrorEnsuringDaemonSet", err.Error())
			return ctrl.Result{}, err
		}
		_ = r.updateStatus(ctx, &dcn, "Ready", "Resources created")
		return ctrl.Result{}, nil
	}

	fullName := fmt.Sprintf("%s/%s", nadNamespace, nadName)
	inUse, users, err := r.isNetworkInUse(ctx, fullName, nadName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inUse {
		msg := fmt.Sprintf("Network in use by %d objects: %s", len(users), strings.Join(users, ","))
		_ = r.updateStatus(ctx, &dcn, "DeletionBlocked", msg)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if err := r.deleteBridgeDaemonSet(ctx, bridgeName); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.deleteNAD(ctx, nadNamespace, nadName); err != nil {
		return ctrl.Result{}, err
	}

	dcn.SetFinalizers(removeString(dcn.GetFinalizers(), finalizerName))
	if err := r.Update(ctx, &dcn); err != nil {
		return ctrl.Result{}, err
	}
	_ = r.updateStatus(ctx, &dcn, "Deleted", "Cleaned up resources")
	logger.Info("Deleted dummy network resources and removed finalizer")
	return ctrl.Result{}, nil
}

func (r *DummyClusterNetworkReconciler) updateStatus(ctx context.Context, obj *unstructured.Unstructured, phase, reason string) error {
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"phase":  phase,
		"reason": reason,
	}, "status")
	return r.Status().Update(ctx, obj)
}

func (r *DummyClusterNetworkReconciler) ensureNetworkAttachmentDefinition(ctx context.Context, namespace, name, bridge string) error {
	var nad unstructured.Unstructured
	nad.SetGroupVersionKind(schema.GroupVersionKind{Group: "k8s.cni.cncf.io", Version: "v1", Kind: "NetworkAttachmentDefinition"})
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &nad)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	nad.Object = map[string]interface{}{
		"apiVersion": "k8s.cni.cncf.io/v1",
		"kind":       "NetworkAttachmentDefinition",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"config": fmt.Sprintf(`{
  "cniVersion":"0.3.1",
  "type":"bridge",
  "bridge":"%s",
  "isGateway": true,
  "ipMasq": true,
  "ipam": {
    "type":"host-local",
    "subnet":"10.10.0.0/24"
  }
}`, bridge),
		},
	}
	return r.Create(ctx, &nad)
}

func (r *DummyClusterNetworkReconciler) ensureBridgeDaemonSet(ctx context.Context, bridge string) error {
	dsName := "dummy-bridge-ensure"
	var ds appsv1.DaemonSet
	err := r.Get(ctx, types.NamespacedName{Namespace: "kube-system", Name: dsName}, &ds)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	ds = appsv1.DaemonSet{
		ObjectMeta: v1.ObjectMeta{
			Name:      dsName,
			Namespace: "kube-system",
			Labels: map[string]string{
				"app": "dummy-bridge-ensure",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{"app": "dummy-bridge-ensure"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{"app": "dummy-bridge-ensure"},
				},
				Spec: corev1.PodSpec{
					HostNetwork: true,
					HostPID:     true,
					Containers: []corev1.Container{
						{
							Name:  "ensure-bridge",
							Image: "alpine:3.18",
							SecurityContext: &corev1.SecurityContext{
								Privileged: ptrBool(true),
							},
							Command: []string{"/bin/sh", "-c"},
							Args: []string{fmt.Sprintf(`BRIDGE="%s"
while true; do
  if ip link show "$BRIDGE" >/dev/null 2>&1; then
    ip link set "$BRIDGE" up || true
  else
    ip link add name "$BRIDGE" type bridge || true
    ip link set "$BRIDGE" up || true
  fi
  sleep 30
done`, bridge)},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "lib-modules",
									MountPath: "/lib/modules",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "lib-modules",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/lib/modules",
									Type: newHostPathType(corev1.HostPathDirectory),
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists},
					},
				},
			},
		},
	}
	return r.Create(ctx, &ds)
}

func (r *DummyClusterNetworkReconciler) deleteBridgeDaemonSet(ctx context.Context, bridge string) error {
	dsName := "dummy-bridge-ensure"
	var ds appsv1.DaemonSet
	err := r.Get(ctx, types.NamespacedName{Namespace: "kube-system", Name: dsName}, &ds)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, &ds)
}

func (r *DummyClusterNetworkReconciler) deleteNAD(ctx context.Context, namespace, name string) error {
	var nad unstructured.Unstructured
	nad.SetGroupVersionKind(schema.GroupVersionKind{Group: "k8s.cni.cncf.io", Version: "v1", Kind: "NetworkAttachmentDefinition"})
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &nad); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return r.Delete(ctx, &nad)
}

func (r *DummyClusterNetworkReconciler) isNetworkInUse(ctx context.Context, fullName, shortName string) (bool, []string, error) {
	users := []string{}

	var vmList unstructured.UnstructuredList
	vmList.SetGroupVersionKind(schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineList"})
	if err := r.List(ctx, &vmList); err != nil {
		return false, nil, err
	}
	for _, vm := range vmList.Items {
		if uses, _ := objectUsesNetwork(&vm, fullName, shortName); uses {
			users = append(users, fmt.Sprintf("VM/%s/%s", vm.GetNamespace(), vm.GetName()))
		}
	}

	var vmiList unstructured.UnstructuredList
	vmiList.SetGroupVersionKind(schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstanceList"})
	if err := r.List(ctx, &vmiList); err != nil {
		return false, nil, err
	}
	for _, vmi := range vmiList.Items {
		if uses, _ := objectUsesNetwork(&vmi, fullName, shortName); uses {
			users = append(users, fmt.Sprintf("VMI/%s/%s", vmi.GetNamespace(), vmi.GetName()))
		}
	}

	return len(users) > 0, users, nil
}

func objectUsesNetwork(obj *unstructured.Unstructured, fullName, shortName string) (bool, error) {
	networks, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "networks")
	if err == nil && found {
		for _, n := range networks {
			if m, ok := n.(map[string]interface{}); ok {
				if multus, ok := m["multus"].(map[string]interface{}); ok {
					if networkName, ok := multus["networkName"].(string); ok {
						if networkName == fullName || networkName == shortName || strings.HasSuffix(networkName, "/"+shortName) {
							return true, nil
						}
					}
				}
			}
		}
	}
	ann, _, _ := unstructured.NestedString(obj.Object, "spec", "template", "metadata", "annotations", "k8s.v1.cni.cncf.io/networks")
	if ann != "" {
		parts := strings.Split(ann, ",")
		for _, p := range parts {
			t := strings.TrimSpace(p)
			if t == fullName || t == shortName || strings.HasSuffix(t, "/"+shortName) {
				return true, nil
			}
		}
	}
	return false, nil
}

func ptrBool(b bool) *bool { return &b }

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	var out []string
	for _, item := range slice {
		if item == s {
			continue
		}
		out = append(out, item)
	}
	return out
}

func newHostPathType(t corev1.HostPathType) *corev1.HostPathType {
	pt := t
	return &pt
}