// Package exporter scrapes macOS powermetrics and exposes Prometheus metrics
// for Apple Silicon GPU utilization, power draw, ANE utilization, and
// thermal state, compatible with the Prometheus Operator ServiceMonitor CRD.
//
// Reference: NVIDIA DCGM Exporter serves the same role in the NVIDIA GPU Operator.
package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// ─────────────────────────────────────────────────────────────────────────────
// powermetrics JSON schema (partial)
// ─────────────────────────────────────────────────────────────────────────────

type powermetricsOutput struct {
	GPU struct {
		FreqHz      float64 `json:"freq_hz"`
		IdleRatio   float64 `json:"idle_ratio"`   // 0.0–1.0
		DVFMStates  []struct {
			ActiveUS   int64   `json:"active_us"`
		} `json:"dvfm_states"`
	} `json:"gpu"`
	Processor struct {
		Temperature float64 `json:"package_temperature"`
		ANE struct {
			PowerMW float64 `json:"ane_power_mw"`
		} `json:"ane"`
		GPU struct {
			PowerMW float64 `json:"gpu_power_mw"`
		} `json:"gpu"`
	} `json:"processor"`
	ThermalPressure string `json:"thermal_pressure"` // "Nominal","Fair","Serious","Critical"
}

// ThermalStateToInt converts a macOS thermal_pressure string to an integer (0-3).
func ThermalStateToInt(s string) float64 {
	switch s {
	case "Nominal":
		return 0
	case "Fair":
		return 1
	case "Serious":
		return 2
	case "Critical":
		return 3
	default:
		return 0
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Prometheus metrics descriptors
// ─────────────────────────────────────────────────────────────────────────────

var (
	gpuUtilizationGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "apple",
		Subsystem: "gpu",
		Name:      "utilization_percent",
		Help:      "GPU engine utilization as a percentage (0–100).",
	}, []string{"node", "chip_variant"})

	gpuPowerGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "apple",
		Subsystem: "gpu",
		Name:      "power_watts",
		Help:      "GPU power draw in watts.",
	}, []string{"node", "chip_variant"})

	aneUtilGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "apple",
		Subsystem: "ane",
		Name:      "power_milliwatts",
		Help:      "Apple Neural Engine power draw in milliwatts (proxy for utilization).",
	}, []string{"node", "chip_variant"})

	thermalStateGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "apple",
		Subsystem: "node",
		Name:      "thermal_state",
		Help:      "Node thermal pressure: 0=Nominal 1=Fair 2=Serious 3=Critical.",
	}, []string{"node", "chip_variant"})

	cpuTempGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "apple",
		Subsystem: "cpu",
		Name:      "package_temp_celsius",
		Help:      "CPU package temperature in degrees Celsius.",
	}, []string{"node", "chip_variant"})

	gpuSlotsTotalGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "apple",
		Subsystem: "gpu",
		Name:      "slots_total",
		Help:      "Total logical GPU slots advertised to Kubernetes.",
	}, []string{"node", "chip_variant"})

	gpuSlotsAllocatedGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "apple",
		Subsystem: "gpu",
		Name:      "slots_allocated",
		Help:      "GPU slots currently allocated to pods.",
	}, []string{"node", "chip_variant"})
)

// RegisterMetrics registers all Prometheus collectors.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(
		gpuUtilizationGauge,
		gpuPowerGauge,
		aneUtilGauge,
		thermalStateGauge,
		cpuTempGauge,
		gpuSlotsTotalGauge,
		gpuSlotsAllocatedGauge,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Exporter
// ─────────────────────────────────────────────────────────────────────────────

// Exporter scrapes powermetrics and keeps Prometheus gauges up-to-date.
type Exporter struct {
	log         logr.Logger
	nodeName    string
	chipVariant string
	interval    time.Duration
	k8sClient   kubernetes.Interface

	mu          sync.Mutex
	lastSample  *powermetricsOutput
}

// NewExporter constructs an Exporter.
func NewExporter(
	log logr.Logger,
	nodeName, chipVariant string,
	interval time.Duration,
	k8sClient kubernetes.Interface,
) *Exporter {
	return &Exporter{
		log:         log.WithName("exporter"),
		nodeName:    nodeName,
		chipVariant: chipVariant,
		interval:    interval,
		k8sClient:   k8sClient,
	}
}

// Run starts the scrape loop. Blocks until ctx is cancelled.
func (e *Exporter) Run(ctx context.Context) error {
	e.log.Info("Starting powermetrics scrape loop", "interval", e.interval)

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := e.scrape(ctx); err != nil {
				e.log.Error(err, "scrape failed")
			}
		}
	}
}

// scrape runs powermetrics once and updates Prometheus metrics.
func (e *Exporter) scrape(ctx context.Context) error {
	// powermetrics requires root on production nodes.
	// -n 1 = one sample, -i 1000 = 1 second interval (minimum), json output.
	cmd := exec.CommandContext(ctx,
		"powermetrics",
		"--samplers", "gpu_power,cpu_power,thermal",
		"-n", "1",
		"-i", "1000",
		"--format", "json",
	)

	out, err := cmd.Output()
	if err != nil {
		// In test / non-root environments powermetrics may fail; use mock.
		return fmt.Errorf("powermetrics: %w", err)
	}

	var pm powermetricsOutput
	if err := json.Unmarshal(out, &pm); err != nil {
		return fmt.Errorf("parse powermetrics JSON: %w", err)
	}

	// Capture values under lock for thread safety.
	e.mu.Lock()
	e.lastSample = &pm
	utilPct := (1.0 - pm.GPU.IdleRatio) * 100.0
	thermalPressure := pm.ThermalPressure
	e.mu.Unlock()

	labels := prometheus.Labels{"node": e.nodeName, "chip_variant": e.chipVariant}

	gpuUtilizationGauge.With(labels).Set(utilPct)
	gpuPowerGauge.With(labels).Set(pm.Processor.GPU.PowerMW / 1000.0)
	aneUtilGauge.With(labels).Set(pm.Processor.ANE.PowerMW)
	thermalStateGauge.With(labels).Set(ThermalStateToInt(pm.ThermalPressure))
	cpuTempGauge.With(labels).Set(pm.Processor.Temperature)

	// Update node thermal annotation so scheduler extender can read it.
	// Use captured values (not lastSample reference) to avoid race.
	if err := e.annotateNode(ctx, thermalPressure, utilPct); err != nil {
		e.log.Error(err, "failed to annotate node thermal state")
	}

	return nil
}

// UpdateSlotMetrics is called externally (from the device plugin) to update
// slot allocation gauges.
func (e *Exporter) UpdateSlotMetrics(total, allocated int) {
	labels := prometheus.Labels{"node": e.nodeName, "chip_variant": e.chipVariant}
	gpuSlotsTotalGauge.With(labels).Set(float64(total))
	gpuSlotsAllocatedGauge.With(labels).Set(float64(allocated))
}

// annotateNode patches the apple.com/thermal-state and gpu-util-pct annotations
// on this node using a strategic merge patch to avoid clobbering annotations set
// by other controllers (NFD, etc.).
func (e *Exporter) annotateNode(ctx context.Context, thermalState string, utilPct float64) error {
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]string{
				"apple.com/thermal-state": thermalState,
				"apple.com/gpu-util-pct":  strconv.FormatFloat(utilPct, 'f', 1, 64),
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal annotation patch: %w", err)
	}

	_, err = e.k8sClient.CoreV1().Nodes().Patch(
		ctx, e.nodeName, k8stypes.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	return err
}
