// Package scheduler implements a Kubernetes Scheduler Extender for
// apple.com/gpu resources. It filters out unhealthy or thermally degraded
// nodes and prioritizes nodes by GPU utilization and topology.
//
// Reference: https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/apis/extender/v1/types.go
// Reference: Intel GPU Device Plugin scheduler extender
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	extender "k8s.io/kube-scheduler/extender/v1"
)

// ─────────────────────────────────────────────────────────────────────────────
// Extender server
// ─────────────────────────────────────────────────────────────────────────────

// Extender is the HTTP handler for the Kubernetes scheduler extender endpoints.
type Extender struct {
	log       logr.Logger
	k8sClient kubernetes.Interface
}

// NewExtender constructs an Extender.
func NewExtender(log logr.Logger, k8sClient kubernetes.Interface) *Extender {
	return &Extender{
		log:       log.WithName("scheduler-extender"),
		k8sClient: k8sClient,
	}
}

// RegisterHandlers sets up the HTTP mux for the extender endpoints.
func (e *Extender) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/filter", e.handleFilter)
	mux.HandleFunc("/prioritize", e.handlePrioritize)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ok") //nolint:errcheck
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Filter — remove nodes where metal-proxy is unhealthy or node is Critical
// ─────────────────────────────────────────────────────────────────────────────

func (e *Extender) handleFilter(w http.ResponseWriter, r *http.Request) {
	var args extender.ExtenderArgs
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	eligible := []corev1.Node{}
	failed := extender.FailedNodesMap{}

	for i := range args.Nodes.Items {
		node := &args.Nodes.Items[i]
		if reason := e.filterNode(ctx, node); reason != "" {
			failed[node.Name] = reason
			e.log.Info("filtered node", "node", node.Name, "reason", reason)
		} else {
			eligible = append(eligible, *node)
		}
	}

	result := extender.ExtenderFilterResult{
		Nodes:       &corev1.NodeList{Items: eligible},
		FailedNodes: failed,
	}
	if err := json.NewEncoder(w).Encode(result); err != nil {
		e.log.Error(err, "encode filter response")
	}
}

// filterNode returns an empty string if the node is eligible, or a reason string.
func (e *Extender) filterNode(ctx context.Context, node *corev1.Node) string {
	annotations := node.Annotations

	// Check thermal state.
	if thermal, ok := annotations["apple.com/thermal-state"]; ok {
		if thermal == "Critical" {
			return "node thermal state is Critical"
		}
	}

	// Check device-plugin health: look for a Ready metal-device-plugin pod.
	pods, err := e.k8sClient.CoreV1().Pods("apple-gpu-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app=metal-device-plugin",
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", node.Name),
	})
	if err != nil {
		return fmt.Sprintf("cannot check device-plugin health: %v", err)
	}
	if len(pods.Items) == 0 {
		return "metal-device-plugin pod not found on this node"
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			return fmt.Sprintf("metal-device-plugin pod is %s", pod.Status.Phase)
		}
	}

	return "" // node is eligible
}

// ─────────────────────────────────────────────────────────────────────────────
// Prioritize — score nodes by GPU utilization and thermal headroom
// ─────────────────────────────────────────────────────────────────────────────

func (e *Extender) handlePrioritize(w http.ResponseWriter, r *http.Request) {
	var args extender.ExtenderArgs
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	scores := extender.HostPriorityList{}

	for i := range args.Nodes.Items {
		node := &args.Nodes.Items[i]
		score := e.scoreNode(node)
		scores = append(scores, extender.HostPriority{
			Host:  node.Name,
			Score: score,
		})
		e.log.V(2).Info("prioritize node", "node", node.Name, "score", score)
	}

	// Sort descending for logging (k8s doesn't require sorted output).
	sort.Slice(scores, func(i, j int) bool { return scores[i].Score > scores[j].Score })

	if err := json.NewEncoder(w).Encode(scores); err != nil {
		e.log.Error(err, "encode prioritize response")
	}
}

// scoreNode returns a score 0–100 for the node (higher = more preferred).
// Scoring factors:
//   40% — inverse GPU utilization (less-loaded = higher score)
//   40% — inverse thermal pressure (cooler = higher score)
//   20% — available GPU slots (more available = higher score)
func (e *Extender) scoreNode(node *corev1.Node) int64 {
	annotations := node.Annotations
	labels := node.Labels

	// GPU utilization (0–100); lower is better.
	gpuUtil := float64(0)
	if util, ok := annotations["apple.com/gpu-util-pct"]; ok {
		if v, err := strconv.ParseFloat(strings.TrimSpace(util), 64); err == nil {
			gpuUtil = v
		}
	}

	// Thermal state (0–3); lower is better.
	thermalScore := float64(0)
	if t, ok := annotations["apple.com/thermal-state"]; ok {
		switch t {
		case "Fair":
			thermalScore = 33
		case "Serious":
			thermalScore = 66
		case "Critical":
			thermalScore = 100
		}
	}

	// Available GPU slots.
	totalSlots := int64(1)
	if s, ok := labels["apple.com/gpu-slots"]; ok {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
			totalSlots = v
		}
	}

	// Normalize each factor to 0–100.
	utilScore := 100.0 - gpuUtil          // higher = less utilized (better)
	thermalHeadroom := 100.0 - thermalScore // higher = cooler (better)
	slotScore := float64(totalSlots) * 10  // crude proxy, capped below

	// Weighted sum.
	weighted := 0.40*utilScore + 0.40*thermalHeadroom + 0.20*slotScore
	if weighted < 0 {
		weighted = 0
	}
	if weighted > 100 {
		weighted = 100
	}

	return int64(weighted)
}

// Silence unused imports.
var _ = corev1.NodeSpec{}
var _ = metav1.ListOptions{}
