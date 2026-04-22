// Package proxy provides a Go client for the metal-proxy gRPC service.
// Workloads import this package to submit Metal compute jobs from inside
// a container (via the injected Unix socket).
package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// ErrNotImplemented is returned when a method requires generated proto stubs
// that have not been compiled into this binary yet.
var ErrNotImplemented = errors.New("method requires generated proto stubs (run 'make proto')")

// GetDeviceInfo returns information about the Metal GPU on the current node.
// NOTE: This method requires generated proto stubs. Until stubs are generated
// (via `make proto`), it returns ErrNotImplemented.
func (c *Client) GetDeviceInfo(ctx context.Context) (*DeviceInfo, error) {
	// TODO: When proto stubs are generated, replace with:
	//   client := metalpb.NewMetalComputeServiceClient(c.conn)
	//   resp, err := client.GetDeviceInfo(ctx, &metalpb.Empty{})
	return nil, fmt.Errorf("GetDeviceInfo: %w", ErrNotImplemented)
}

// Health returns true if the metal-proxy is healthy and accepting connections.
// Performs an actual TCP-level connectivity check on the underlying connection.
func (c *Client) Health(ctx context.Context) (bool, error) {
	// Check if the gRPC connection is in a valid state.
	state := c.conn.GetState()
	switch state.String() {
	case "SHUTDOWN", "TRANSIENT_FAILURE":
		return false, nil
	}

	// Try to verify at the transport level by getting the socket path
	// and doing a quick dial.
	socket := os.Getenv(DefaultSocketEnv)
	if socket != "" {
		conn, err := net.DialTimeout("unix", socket, 2*time.Second)
		if err != nil {
			return false, nil
		}
		_ = conn.Close()
	}

	return true, nil
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
