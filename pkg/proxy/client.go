// Package proxy provides a Go client for the metal-proxy gRPC service.
// Workloads import this package to submit Metal compute jobs from inside
// a container (via the injected Unix socket).
package proxy

import (
	"context"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const (
	// DefaultSocketEnv is the env var injected by the device plugin.
	DefaultSocketEnv = "METAL_PROXY_SOCKET"

	// DefaultDialTimeout is the maximum time to wait for the socket connection.
	DefaultDialTimeout = 5 * time.Second
)

// ─────────────────────────────────────────────────────────────────────────────
// Client
// ─────────────────────────────────────────────────────────────────────────────

// Client wraps a gRPC connection to the metal-proxy daemon.
// It is safe to use from multiple goroutines.
type Client struct {
	conn *grpc.ClientConn
}

// NewClientFromEnv creates a Client using the socket path from the
// METAL_PROXY_SOCKET environment variable (injected by the device plugin).
func NewClientFromEnv() (*Client, error) {
	socket := os.Getenv(DefaultSocketEnv)
	if socket == "" {
		return nil, fmt.Errorf(
			"env var %s not set — is the pod requesting apple.com/gpu resources?",
			DefaultSocketEnv,
		)
	}
	return NewClient(socket)
}

// NewClient creates a Client connecting to the given Unix socket path.
func NewClient(socketPath string) (*Client, error) {
	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("dial metal-proxy at %s: %w", socketPath, err)
	}
	return &Client{conn: conn}, nil
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// ─────────────────────────────────────────────────────────────────────────────
// JobRequest / JobResult (language-level wrappers for the gRPC proto types)
// ─────────────────────────────────────────────────────────────────────────────

// Backend selects the compute backend.
type Backend int32

const (
	BackendMLX          Backend = 1
	BackendPytorchMPS   Backend = 2
	BackendMetalCompute Backend = 3
)

// Priority is the job queue priority.
type Priority int32

const (
	PriorityNormal Priority = 0
	PriorityHigh   Priority = 1
	PriorityBatch  Priority = 2
)

// JobRequest is the high-level representation of a compute job.
type JobRequest struct {
	JobID          string
	Backend        Backend
	Priority       Priority
	Payload        []byte
	TimeoutSeconds uint32
	Labels         map[string]string
}

// JobResult holds the outcome of a completed job.
type JobResult struct {
	JobID      string
	Succeeded  bool
	Output     []byte
	Error      string
	GPUTimeUs  uint64
	WallTimeUs uint64
}

// ─────────────────────────────────────────────────────────────────────────────
// High-level API methods
// ─────────────────────────────────────────────────────────────────────────────

// DeviceInfo holds static information about the Metal GPU on the current node.
type DeviceInfo struct {
	ChipName           string
	ChipVariant        string
	GPUCores           uint32
	ANETOPS            float32
	UnifiedMemoryBytes uint64
	GPUFamily          string
}

// GetDeviceInfo returns information about the Metal GPU on the current node.
// Useful for workloads that want to adapt to the available hardware.
func (c *Client) GetDeviceInfo(ctx context.Context) (*DeviceInfo, error) {
	// NOTE: When proto stubs are generated (make proto), this will call
	// the generated MetalComputeServiceClient.GetDeviceInfo method.
	// For now we return a placeholder to allow compilation without generated stubs.
	return &DeviceInfo{
		ChipName:    os.Getenv("METAL_CHIP_VARIANT"),
		ChipVariant: os.Getenv("METAL_CHIP_VARIANT"),
	}, nil
}

// Health returns true if the metal-proxy is healthy and accepting jobs.
func (c *Client) Health(ctx context.Context) (bool, error) {
	// Implemented fully once proto stubs are generated.
	// For pre-generation: attempt a ping via the raw conn state.
	state := c.conn.GetState()
	return state.String() != "SHUTDOWN", nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience constructors for common workloads
// ─────────────────────────────────────────────────────────────────────────────

// MLXRequest builds a JobRequest for an MLX inference job.
// modelPath is the path to an MLX model directory (e.g. converted Hugging Face model).
func MLXRequest(jobID, modelPath string, inputJSON []byte) JobRequest {
	return JobRequest{
		JobID:    jobID,
		Backend:  BackendMLX,
		Priority: PriorityNormal,
		Payload:  append([]byte(modelPath+"\n"), inputJSON...),
	}
}

// PyTorchMPSRequest builds a JobRequest for a PyTorch MPS inference job.
// modelBytes should be a serialised TorchScript model (.pt).
func PyTorchMPSRequest(jobID string, modelBytes []byte) JobRequest {
	return JobRequest{
		JobID:    jobID,
		Backend:  BackendPytorchMPS,
		Priority: PriorityNormal,
		Payload:  modelBytes,
	}
}
