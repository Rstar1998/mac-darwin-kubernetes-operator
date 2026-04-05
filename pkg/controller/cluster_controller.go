// Package controller implements the AppleGPUCluster reconciler — the brain of
// the Apple GPU Operator. It watches AppleGPUCluster CRs and ensures all
// operator-managed DaemonSets (metal-proxy, device-plugin, exporter) and the
// scheduler-extender Deployment are in the desired state.
//
// Pattern reference: Intel Device Plugins Operator (github.com/intel/intel-device-plugins-for-kubernetes)
package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	gpuv1 "github.com/gpu-operator-mac/apple-gpu-operator/pkg/api/v1alpha1"
)

const (
	// Namespace where all operator-managed pods run.
	OperatorNamespace = "apple-gpu-system"

	// FinalizerName is the finalizer added to AppleGPUCluster to enable cleanup.
	FinalizerName = "gpu.apple.com/finalizer"
)

// ─────────────────────────────────────────────────────────────────────────────
// AppleGPUClusterReconciler
// ─────────────────────────────────────────────────────────────────────────────

// AppleGPUClusterReconciler reconciles AppleGPUCluster objects.
type AppleGPUClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gpu.apple.com,resources=applegpuclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gpu.apple.com,resources=applegpuclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets;deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create

// Reconcile is the main reconciliation loop.
func (r *AppleGPUClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Fetch the AppleGPUCluster CR.
	cluster := &gpuv1.AppleGPUCluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, cluster); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion.
	if !cluster.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, cluster)
	}

	// Add finalizer.
	if !controllerutil.ContainsFinalizer(cluster, FinalizerName) {
		controllerutil.AddFinalizer(cluster, FinalizerName)
		if err := r.Client.Update(ctx, cluster); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Ensure operator namespace exists.
	if err := r.ensureNamespace(ctx); err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile each managed component.
	if err := r.reconcileMetalProxy(ctx, cluster); err != nil {
		log.Error(err, "reconcile metal-proxy DaemonSet failed")
		return ctrl.Result{}, err
	}
	if err := r.reconcileDevicePlugin(ctx, cluster); err != nil {
		log.Error(err, "reconcile device-plugin DaemonSet failed")
		return ctrl.Result{}, err
	}
	if err := r.reconcileExporter(ctx, cluster); err != nil {
		log.Error(err, "reconcile exporter DaemonSet failed")
		return ctrl.Result{}, err
	}
	if cluster.Spec.SchedulerExtenderEnabled {
		if err := r.reconcileSchedulerExtender(ctx, cluster); err != nil {
			log.Error(err, "reconcile scheduler-extender Deployment failed")
			return ctrl.Result{}, err
		}
	}

	// Update status.
	if err := r.updateStatus(ctx, cluster); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconcile complete", "cluster", cluster.Name)
	return ctrl.Result{}, nil
}

// ─── Component reconcilers ───────────────────────────────────────────────────

// reconcileMetalProxy ensures the metal-proxy DaemonSet is in the desired state.
// metal-proxy runs as a privileged host-network DaemonSet to access Metal APIs.
func (r *AppleGPUClusterReconciler) reconcileMetalProxy(
	ctx context.Context,
	cluster *gpuv1.AppleGPUCluster,
) error {
	hostPathSocket := corev1.HostPathDirectoryOrCreate
	privileged := true

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "metal-proxy",
			Namespace: OperatorNamespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ds, func() error {
		ds.Spec = appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "metal-proxy"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "metal-proxy"},
					Annotations: map[string]string{
						"apple.com/component": "metal-proxy",
					},
				},
				Spec: corev1.PodSpec{
					HostNetwork:        true,
					HostPID:            true,
					ServiceAccountName: "metal-proxy",
					NodeSelector:       r.nodeSelector(cluster),
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists},
					},
					Containers: []corev1.Container{{
						Name:            "metal-proxy",
						Image:           cluster.Spec.MetalProxyImage,
						ImagePullPolicy: corev1.PullPolicy(cluster.Spec.ImagePullPolicy),
						SecurityContext: &corev1.SecurityContext{
							Privileged: &privileged,
						},
						Ports: []corev1.ContainerPort{{
							Name:          "grpc",
							ContainerPort: 50051,
							Protocol:      corev1.ProtocolTCP,
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "proxy-socket",
							MountPath: "/var/run/metal-proxy",
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								Exec: &corev1.ExecAction{
									Command: []string{"/usr/local/bin/metal-health"},
								},
							},
							InitialDelaySeconds: 15,
							PeriodSeconds:       30,
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "proxy-socket",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/var/run/metal-proxy",
								Type: &hostPathSocket,
							},
						},
					}},
				},
			},
		}
		return controllerutil.SetControllerReference(cluster, ds, r.Scheme)
	})
	return err
}

// reconcileDevicePlugin ensures the metal-device-plugin DaemonSet.
func (r *AppleGPUClusterReconciler) reconcileDevicePlugin(
	ctx context.Context,
	cluster *gpuv1.AppleGPUCluster,
) error {
	hostPathDir := corev1.HostPathDirectory
	hostPathSocket := corev1.HostPathDirectoryOrCreate

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "metal-device-plugin",
			Namespace: OperatorNamespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ds, func() error {
		ds.Spec = appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "metal-device-plugin"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "metal-device-plugin"},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "metal-device-plugin",
					NodeSelector:       r.nodeSelector(cluster),
					Containers: []corev1.Container{{
						Name:            "metal-device-plugin",
						Image:           cluster.Spec.DevicePluginImage,
						ImagePullPolicy: corev1.PullPolicy(cluster.Spec.ImagePullPolicy),
						Env: []corev1.EnvVar{
							{
								Name: "NODE_NAME",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
								},
							},
							{
								Name:  "CORES_PER_SLOT",
								Value: fmt.Sprintf("%d", cluster.Spec.CoresPerSlot),
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "device-plugin", MountPath: "/var/lib/kubelet/device-plugins"},
							{Name: "proxy-socket", MountPath: "/var/run/metal-proxy"},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "device-plugin",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/kubelet/device-plugins",
									Type: &hostPathDir,
								},
							},
						},
						{
							Name: "proxy-socket",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/run/metal-proxy",
									Type: &hostPathSocket,
								},
							},
						},
					},
				},
			},
		}
		return controllerutil.SetControllerReference(cluster, ds, r.Scheme)
	})
	return err
}

// reconcileExporter ensures the metal-exporter DaemonSet.
func (r *AppleGPUClusterReconciler) reconcileExporter(
	ctx context.Context,
	cluster *gpuv1.AppleGPUCluster,
) error {
	if !cluster.Spec.ExporterEnabled {
		return nil
	}
	privileged := true

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "metal-exporter",
			Namespace: OperatorNamespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ds, func() error {
		ds.Spec = appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "metal-exporter"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                    "metal-exporter",
						"prometheus-scrape":      "true",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "metal-exporter",
					NodeSelector:       r.nodeSelector(cluster),
					HostPID:            true,
					Containers: []corev1.Container{{
						Name:            "metal-exporter",
						Image:           cluster.Spec.ExporterImage,
						ImagePullPolicy: corev1.PullPolicy(cluster.Spec.ImagePullPolicy),
						SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
						Ports: []corev1.ContainerPort{{
							Name:          "metrics",
							ContainerPort: 9100,
							Protocol:      corev1.ProtocolTCP,
						}},
						Env: []corev1.EnvVar{{
							Name: "NODE_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
							},
						}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/metrics",
									Port: intstr.FromInt(9100),
								},
							},
						},
					}},
				},
			},
		}
		return controllerutil.SetControllerReference(cluster, ds, r.Scheme)
	})
	return err
}

// reconcileSchedulerExtender ensures the scheduler extender Deployment.
func (r *AppleGPUClusterReconciler) reconcileSchedulerExtender(
	ctx context.Context,
	cluster *gpuv1.AppleGPUCluster,
) error {
	replicas := int32(2) // HA: 2 replicas

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "metal-scheduler-extender",
			Namespace: OperatorNamespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		deploy.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "metal-scheduler-extender"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "metal-scheduler-extender"},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "metal-scheduler-extender",
					Containers: []corev1.Container{{
						Name:            "extender",
						Image:           cluster.Spec.SchedulerExtenderImage,
						ImagePullPolicy: corev1.PullPolicy(cluster.Spec.ImagePullPolicy),
						Ports: []corev1.ContainerPort{{
							Name:          "http",
							ContainerPort: 8888,
						}},
					}},
				},
			},
		}
		return controllerutil.SetControllerReference(cluster, deploy, r.Scheme)
	})
	return err
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// nodeSelector merges the cluster-level node selector with the default
// apple.com/chip-family label so DaemonSets only land on M-series nodes.
func (r *AppleGPUClusterReconciler) nodeSelector(cluster *gpuv1.AppleGPUCluster) map[string]string {
	sel := map[string]string{"apple.com/chip-family": "m-series"}
	for k, v := range cluster.Spec.NodeSelector {
		sel[k] = v
	}
	return sel
}

// ensureNamespace creates the operator namespace if it doesn't exist.
func (r *AppleGPUClusterReconciler) ensureNamespace(ctx context.Context) error {
	ns := &corev1.Namespace{}
	err := r.Client.Get(ctx, client.ObjectKey{Name: OperatorNamespace}, ns)
	if errors.IsNotFound(err) {
		return r.Client.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: OperatorNamespace},
		})
	}
	return err
}

// updateStatus patches the AppleGPUCluster status with node counts.
func (r *AppleGPUClusterReconciler) updateStatus(
	ctx context.Context,
	cluster *gpuv1.AppleGPUCluster,
) error {
	nodeList := &corev1.NodeList{}
	if err := r.Client.List(ctx, nodeList, client.MatchingLabels{"apple.com/chip-family": "m-series"}); err != nil {
		return err
	}

	patch := cluster.DeepCopy()
	patch.Status.TotalNodes = int32(len(nodeList.Items))
	patch.Status.ObservedGeneration = cluster.Generation

	readyCount := int32(0)
	for _, node := range nodeList.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				readyCount++
			}
		}
	}
	patch.Status.ReadyNodes = readyCount

	return r.Client.Status().Update(ctx, patch)
}

// handleDeletion removes the finalizer once owned resources are cleaned up.
func (r *AppleGPUClusterReconciler) handleDeletion(
	ctx context.Context,
	cluster *gpuv1.AppleGPUCluster,
) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(cluster, FinalizerName)
	return ctrl.Result{}, r.Client.Update(ctx, cluster)
}

// SetupWithManager registers the reconciler with the controller-runtime Manager.
func (r *AppleGPUClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gpuv1.AppleGPUCluster{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
