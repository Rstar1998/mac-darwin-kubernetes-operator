// Package plugin implements the Kubernetes Device Plugin API v1beta1 for
// Apple M-series GPU resources. It advertises the logical resource
// "apple.com/gpu" to kubelet and injects the metal-proxy Unix socket into
// allocated containers via AllocateResponse.
//
// Architecture reference:
//   - NVIDIA k8s-device-plugin (https://github.com/NVIDIA/k8s-device-plugin)
//   - K8s Device Plugin spec v1beta1
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"google.golang.org/grpc"

	"github.com/gpu-operator-mac/apple-gpu-operator/pkg/version"
)

const (
	// ResourceName is the extended resource advertised to Kubernetes.
	ResourceName = "apple.com/gpu"

	// PluginSocket is the path on the kubelet plugin directory where we register.
	PluginSocket = "metal-device-plugin.sock"

	// KubeletSocket is the standard kubelet device plugin registration socket.
	KubeletSocket = "kubelet.sock"

	// DevicePluginPath is the kubelet device-plugin directory.
	DevicePluginPath = "/var/lib/kubelet/device-plugins/"

	// MetalProxySocketDir is where metal-proxy exposes its gRPC socket.
	MetalProxySocketDir = "/var/run/metal-proxy"

	// MetalProxySocketPath is the metal-proxy Unix socket path.
	MetalProxySocketPath = "/var/run/metal-proxy/metal.sock"

	// ContainerSocketPath is the in-container path injected by Allocate.
	ContainerSocketPath = "/dev/metal/proxy.sock"

	// DefaultCoresPerSlot is the default physical GPU cores per logical slot.
	// 0 means 1 slot per physical device (entire GPU).
	DefaultCoresPerSlot = 0

	// HealthCheckInterval is how often we re-probe device health.
	HealthCheckInterval = 10 * time.Second
)

// ─────────────────────────────────────────────────────────────────────────────
// System Profiler types (for system_profiler JSON parsing)
// ─────────────────────────────────────────────────────────────────────────────

// SPDisplaysDataType models the relevant fields from system_profiler output.
type spDisplaysData struct {
	SPDisplaysDataType []spDisplay `json:"SPDisplaysDataType"`
}

type spDisplay struct {
	ChipModel        string `json:"sppci_model"`
	CoreCount        string `json:"sppci_cores"`
	MetalVersion     string `json:"sppci_metal"`
	VRAM             string `json:"sppci_vram"`
}

// ─────────────────────────────────────────────────────────────────────────────
// AppleGPUInfo holds detected GPU information.
// ─────────────────────────────────────────────────────────────────────────────

// AppleGPUInfo holds information about the M-series GPU detected on this node.
type AppleGPUInfo struct {
	// ChipModel e.g. "Apple M3 Max"
	ChipModel string
	// ChipVariant e.g. "m3-max"
	ChipVariant string
	// GPUCores is the physical GPU core count.
	GPUCores int
	// GPUFamily e.g. "apple9"
	GPUFamily string
	// UnifiedMemoryBytes is the total unified memory.
	UnifiedMemoryBytes int64
}

// coreCountRegex extracts numeric core count from strings like "30", "30 cores", "40-core".
var coreCountRegex = regexp.MustCompile(`(\d+)`)

// parseCoreCount robustly extracts the numeric core count from system_profiler output.
// Handles: "30", "30 cores", "40-core", " 30 ", or other macOS version variations.
func parseCoreCount(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}

	// Direct parse (most common case).
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}

	// Extract first numeric sequence from the string.
	matches := coreCountRegex.FindStringSubmatch(raw)
	if len(matches) >= 2 {
		if n, err := strconv.Atoi(matches[1]); err == nil {
			return n
		}
	}

	return 0
}

// DiscoverGPU uses system_profiler to detect the Apple Silicon GPU.
// Compatible with M1 through latest M-series chips on macOS 14+.
func DiscoverGPU() (*AppleGPUInfo, error) {
	// Use a timeout to prevent hanging if system_profiler is unresponsive.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx,
		"system_profiler", "SPDisplaysDataType", "-json",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("system_profiler failed: %w", err)
	}

	var data spDisplaysData
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("failed to parse system_profiler output: %w", err)
	}

	for _, d := range data.SPDisplaysDataType {
		if !strings.Contains(d.ChipModel, "Apple") {
			continue
		}

		cores := parseCoreCount(d.CoreCount)
		info := &AppleGPUInfo{
			ChipModel:   d.ChipModel,
			ChipVariant: normaliseChipVariant(d.ChipModel),
			GPUCores:    cores,
			GPUFamily:   d.MetalVersion,
		}
		return info, nil
	}
	return nil, fmt.Errorf("no Apple Silicon GPU found via system_profiler")
}

// normaliseChipVariant maps "Apple M3 Max" → "m3-max".
func normaliseChipVariant(model string) string {
	lower := strings.ToLower(model)
	lower = strings.TrimPrefix(lower, "apple ")
	return strings.ReplaceAll(lower, " ", "-")
}

// ─────────────────────────────────────────────────────────────────────────────
// MetalDevicePlugin implements pluginapi.DevicePluginServer
// ─────────────────────────────────────────────────────────────────────────────

// MetalDevicePlugin is the gRPC server that registers with kubelet and
// manages apple.com/gpu device allocation.
type MetalDevicePlugin struct {
	pluginapi.UnimplementedDevicePluginServer

	log          logr.Logger
	gpuInfo      *AppleGPUInfo
	coresPerSlot int
	k8sClient    kubernetes.Interface
	nodeName     string

	server   *grpc.Server
	stopCh   chan struct{}
}

// NewMetalDevicePlugin constructs a MetalDevicePlugin.
func NewMetalDevicePlugin(
	log logr.Logger,
	gpuInfo *AppleGPUInfo,
	coresPerSlot int,
	k8sClient kubernetes.Interface,
	nodeName string,
) *MetalDevicePlugin {
	return &MetalDevicePlugin{
		log:          log.WithName("device-plugin"),
		gpuInfo:      gpuInfo,
		coresPerSlot: coresPerSlot,
		k8sClient:    k8sClient,
		nodeName:     nodeName,
		stopCh:       make(chan struct{}),
	}
}

// TotalSlots returns the number of logical GPU slots on this node.
func (p *MetalDevicePlugin) TotalSlots() int {
	if p.coresPerSlot <= 0 {
		return 1
	}
	slots := p.gpuInfo.GPUCores / p.coresPerSlot
	if slots == 0 {
		return 1 // always advertise at least one slot
	}
	return slots
}

// buildDevices returns the full device list for ListAndWatch.
func (p *MetalDevicePlugin) buildDevices() []*pluginapi.Device {
	slots := p.TotalSlots()
	devs := make([]*pluginapi.Device, slots)
	for i := 0; i < slots; i++ {
		devs[i] = &pluginapi.Device{
			ID:     fmt.Sprintf("%s-slot-%d", p.gpuInfo.ChipVariant, i),
			Health: pluginapi.Healthy,
		}
	}
	return devs
}

// isProxyHealthy returns true if the metal-proxy socket exists and is reachable.
func (p *MetalDevicePlugin) isProxyHealthy() bool {
	if _, err := os.Stat(MetalProxySocketPath); os.IsNotExist(err) {
		return false
	}
	conn, err := net.DialTimeout("unix", MetalProxySocketPath, 2*time.Second)
	if err != nil {
		return false
	}
	if err := conn.Close(); err != nil {
		return false
	}
	return true
}

// ─── pluginapi.DevicePluginServer implementation ─────────────────────────────

// GetDevicePluginOptions returns options for this device plugin.
func (p *MetalDevicePlugin) GetDevicePluginOptions(
	_ context.Context,
	_ *pluginapi.Empty,
) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{
		GetPreferredAllocationAvailable: true,
	}, nil
}

// ListAndWatch streams device health updates to kubelet.
func (p *MetalDevicePlugin) ListAndWatch(
	_ *pluginapi.Empty,
	stream pluginapi.DevicePlugin_ListAndWatchServer,
) error {
	p.log.Info("ListAndWatch started", "slots", p.TotalSlots(), "chip", p.gpuInfo.ChipModel)

	ticker := time.NewTicker(HealthCheckInterval)
	defer ticker.Stop()

	for {
		devs := p.buildDevices()
		proxyOK := p.isProxyHealthy()
		if !proxyOK {
			p.log.Info("metal-proxy unhealthy — marking all devices Unhealthy")
			for _, d := range devs {
				d.Health = pluginapi.Unhealthy
			}
		}

		if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: devs}); err != nil {
			return err
		}

		select {
		case <-p.stopCh:
			return nil
		case <-ticker.C:
		}
	}
}

// Allocate is called by kubelet when a pod requests apple.com/gpu.
// We inject the metal-proxy Unix socket and relevant env vars.
func (p *MetalDevicePlugin) Allocate(
	_ context.Context,
	req *pluginapi.AllocateRequest,
) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{}

	for _, container := range req.ContainerRequests {
		p.log.Info("Allocate", "devices", container.DevicesIds)

		// Ensure proxy socket directory exists
		if err := os.MkdirAll(MetalProxySocketDir, 0755); err != nil {
			return nil, fmt.Errorf("cannot create proxy socket dir: %w", err)
		}

		resp.ContainerResponses = append(resp.ContainerResponses,
			&pluginapi.ContainerAllocateResponse{
				Envs: map[string]string{
					"METAL_PROXY_SOCKET":   ContainerSocketPath,
					"METAL_CHIP_VARIANT":   p.gpuInfo.ChipVariant,
					"METAL_GPU_CORES":      strconv.Itoa(p.gpuInfo.GPUCores),
					"METAL_OPERATOR_VER":   version.Version,
				},
				Mounts: []*pluginapi.Mount{
					{
						ContainerPath: ContainerSocketPath,
						HostPath:      MetalProxySocketPath,
						ReadOnly:      false,
					},
				},
			})
	}
	return resp, nil
}

// GetPreferredAllocation allows topology-aware allocation (future: NUMA).
func (p *MetalDevicePlugin) GetPreferredAllocation(
	_ context.Context,
	req *pluginapi.PreferredAllocationRequest,
) (*pluginapi.PreferredAllocationResponse, error) {
	resp := &pluginapi.PreferredAllocationResponse{}
	for _, r := range req.ContainerRequests {
		// Return first N available devices as preferred.
		preferred := r.AvailableDeviceIDs
		if int32(len(preferred)) > r.AllocationSize {
			preferred = preferred[:r.AllocationSize]
		}
		resp.ContainerResponses = append(resp.ContainerResponses,
			&pluginapi.ContainerPreferredAllocationResponse{
				DeviceIDs: preferred,
			})
	}
	return resp, nil
}

// ─── Node labelling ──────────────────────────────────────────────────────────

// LabelNode sets apple.com/* labels on the node to make NFD-style hardware
// facts available to schedulers and node affinity rules.
// Uses JSON merge-patch to avoid clobbering labels from other controllers.
func (p *MetalDevicePlugin) LabelNode(ctx context.Context) error {
	patchData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]string{
				"apple.com/chip-variant":   p.gpuInfo.ChipVariant,
				"apple.com/gpu-core-count": strconv.Itoa(p.gpuInfo.GPUCores),
				"apple.com/chip-family":    "m-series",
				"apple.com/gpu-family":     p.gpuInfo.GPUFamily,
				"apple.com/gpu-slots":      strconv.Itoa(p.TotalSlots()),
			},
		},
	}

	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("marshal label patch: %w", err)
	}

	_, err = p.k8sClient.CoreV1().Nodes().Patch(
		ctx, p.nodeName, k8stypes.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	return err
}

// ─── gRPC server lifecycle ───────────────────────────────────────────────────

// Start starts the device plugin gRPC server and registers with kubelet.
func (p *MetalDevicePlugin) Start(ctx context.Context) error {
	socketPath := filepath.Join(DevicePluginPath, PluginSocket)
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on plugin socket: %w", err)
	}

	p.server = grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(p.server, p)

	go func() {
		if err := p.server.Serve(lis); err != nil {
			p.log.Error(err, "device-plugin gRPC server exited")
		}
	}()

	// Label the node before registering so labels are ready by the time
	// the scheduler sees the new resource.
	if err := p.LabelNode(ctx); err != nil {
		p.log.Error(err, "node labelling failed (non-fatal)")
	}

	return p.register(ctx)
}

// register calls kubelet's Registration service.
func (p *MetalDevicePlugin) register(ctx context.Context) (retErr error) {
	kubeletSock := filepath.Join(DevicePluginPath, KubeletSocket)

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(dialCtx, "unix://"+kubeletSock, //nolint:staticcheck
		grpc.WithInsecure(), //nolint:staticcheck
	)
	if err != nil {
		return fmt.Errorf("connect to kubelet: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close kubelet conn: %w", err)
		}
	}()

	client := pluginapi.NewRegistrationClient(conn)
	_, err = client.Register(ctx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     PluginSocket,
		ResourceName: ResourceName,
		Options: &pluginapi.DevicePluginOptions{
			GetPreferredAllocationAvailable: true,
		},
	})
	return err
}

// Stop shuts down the gRPC server.
func (p *MetalDevicePlugin) Stop() {
	close(p.stopCh)
	if p.server != nil {
		p.server.Stop()
	}
}

// Ensure MetalDevicePlugin satisfies the interface at compile time.
var _ pluginapi.DevicePluginServer = (*MetalDevicePlugin)(nil)
