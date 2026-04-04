package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ─────────────────────────────────────────────────────────────────────────────
// AppleGPUCluster — cluster-wide operator configuration CRD
// ─────────────────────────────────────────────────────────────────────────────

// AppleGPUClusterSpec defines the desired state of the operator-managed stack.
type AppleGPUClusterSpec struct {
	// CoresPerSlot controls how many physical GPU cores map to one Kubernetes
	// apple.com/gpu unit.  Default: 10.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	CoresPerSlot int32 `json:"coresPerSlot,omitempty"`

	// MetalProxyImage is the container image for the metal-proxy DaemonSet.
	// +kubebuilder:default="ghcr.io/gpu-operator-mac/metal-proxy:latest"
	MetalProxyImage string `json:"metalProxyImage,omitempty"`

	// DevicePluginImage is the container image for the metal-device-plugin DaemonSet.
	// +kubebuilder:default="ghcr.io/gpu-operator-mac/metal-device-plugin:latest"
	DevicePluginImage string `json:"devicePluginImage,omitempty"`

	// ExporterImage is the container image for the metal-exporter DaemonSet.
	// +kubebuilder:default="ghcr.io/gpu-operator-mac/metal-exporter:latest"
	ExporterImage string `json:"exporterImage,omitempty"`

	// ExporterEnabled controls whether the Prometheus exporter DaemonSet is deployed.
	// +kubebuilder:default=true
	ExporterEnabled bool `json:"exporterEnabled,omitempty"`

	// SchedulerExtenderEnabled controls whether the custom scheduler extender Deployment
	// is created and the KubeSchedulerConfiguration is applied.
	// +kubebuilder:default=true
	SchedulerExtenderEnabled bool `json:"schedulerExtenderEnabled,omitempty"`

	// SchedulerExtenderImage is the container image for the scheduler extender.
	// +kubebuilder:default="ghcr.io/gpu-operator-mac/metal-scheduler:latest"
	SchedulerExtenderImage string `json:"schedulerExtenderImage,omitempty"`

	// NodeSelector restricts which nodes receive the DaemonSets.
	// Defaults to selecting nodes labelled apple.com/chip-family=m-series.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// ImagePullPolicy applied to all operator-managed containers. Default: IfNotPresent.
	// +kubebuilder:default="IfNotPresent"
	ImagePullPolicy string `json:"imagePullPolicy,omitempty"`
}

// AppleGPUClusterStatus reflects the observed state.
type AppleGPUClusterStatus struct {
	// Conditions lists standard Kubernetes-style status conditions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// TotalNodes is the number of Apple Silicon nodes detected in the cluster.
	TotalNodes int32 `json:"totalNodes,omitempty"`

	// ReadyNodes is the number of nodes where all DaemonSet pods are Running.
	ReadyNodes int32 `json:"readyNodes,omitempty"`

	// TotalGPUSlots is the cluster-wide sum of advertised apple.com/gpu slots.
	TotalGPUSlots int64 `json:"totalGPUSlots,omitempty"`

	// AllocatedGPUSlots is the number of slots currently claimed by pods.
	AllocatedGPUSlots int64 `json:"allocatedGPUSlots,omitempty"`

	// ObservedGeneration echoes the .metadata.generation this status was calculated for.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=agc
// +kubebuilder:printcolumn:name="Ready Nodes",type=integer,JSONPath=".status.readyNodes"
// +kubebuilder:printcolumn:name="GPU Slots",type=integer,JSONPath=".status.totalGPUSlots"
// +kubebuilder:printcolumn:name="Allocated",type=integer,JSONPath=".status.allocatedGPUSlots"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AppleGPUCluster is the top-level cluster-scoped configuration resource.
type AppleGPUCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AppleGPUClusterSpec   `json:"spec,omitempty"`
	Status AppleGPUClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AppleGPUClusterList contains a list of AppleGPUCluster.
type AppleGPUClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AppleGPUCluster `json:"items"`
}

// ─────────────────────────────────────────────────────────────────────────────
// MetalWorkload — per-workload scheduling hint CRD
// ─────────────────────────────────────────────────────────────────────────────

// MetalBackend selects the compute backend.
// +kubebuilder:validation:Enum=mlx;pytorch-mps;metal-compute
type MetalBackend string

const (
	BackendMLX          MetalBackend = "mlx"
	BackendPytorchMPS   MetalBackend = "pytorch-mps"
	BackendMetalCompute MetalBackend = "metal-compute"
)

// MetalPriority is the job scheduling priority.
// +kubebuilder:validation:Enum=high;normal;batch
type MetalPriority string

const (
	PriorityHigh   MetalPriority = "high"
	PriorityNormal MetalPriority = "normal"
	PriorityBatch  MetalPriority = "batch"
)

// ThermalThrottlePolicy controls what happens to a running job when the node
// enters a thermal-throttled state.
// +kubebuilder:validation:Enum=pause;requeue;continue
type ThermalThrottlePolicy string

const (
	ThrottlePause    ThermalThrottlePolicy = "pause"
	ThrottleRequeue  ThermalThrottlePolicy = "requeue"
	ThrottleContinue ThermalThrottlePolicy = "continue"
)

// MetalWorkloadSpec defines scheduling and backend hints for GPU workloads.
type MetalWorkloadSpec struct {
	// GPUSlots is the number of apple.com/gpu units this workload requests.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	GPUSlots int32 `json:"gpuSlots,omitempty"`

	// Backend selects the Metal compute backend.
	// +kubebuilder:default=mlx
	Backend MetalBackend `json:"backend,omitempty"`

	// Priority is the job queue priority sent to metal-proxy.
	// +kubebuilder:default=normal
	Priority MetalPriority `json:"priority,omitempty"`

	// ThermalThrottlePolicy controls workload behaviour during thermal throttling.
	// +kubebuilder:default=pause
	ThermalThrottlePolicy ThermalThrottlePolicy `json:"thermalThrottlePolicy,omitempty"`

	// PodSelector matches the Pods this MetalWorkload applies to.
	PodSelector map[string]string `json:"podSelector,omitempty"`
}

// MetalWorkloadStatus reflects observed job state.
type MetalWorkloadStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	AssignedNode       string             `json:"assignedNode,omitempty"`
	ActiveJobIDs       []string           `json:"activeJobIDs,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mw
// +kubebuilder:printcolumn:name="Backend",type=string,JSONPath=".spec.backend"
// +kubebuilder:printcolumn:name="Slots",type=integer,JSONPath=".spec.gpuSlots"
// +kubebuilder:printcolumn:name="Priority",type=string,JSONPath=".spec.priority"
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=".status.assignedNode"

// MetalWorkload carries per-workload GPU scheduling and backend hints.
type MetalWorkload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MetalWorkloadSpec   `json:"spec,omitempty"`
	Status MetalWorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MetalWorkloadList contains a list of MetalWorkload.
type MetalWorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MetalWorkload `json:"items"`
}
