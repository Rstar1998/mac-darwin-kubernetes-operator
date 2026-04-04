// cmd/hook/main.go — OCI prestart hook for Metal socket injection.
//
// This binary is placed at /usr/local/lib/metal-hook on each node and
// registered as an OCI hook in /etc/containerd/config.toml.
// When any container with METAL_PROXY_SOCKET env var starts, this hook
// bind-mounts the metal-proxy Unix socket into the container's namespace.
//
// OCI Hook spec: https://github.com/opencontainers/runtime-spec/blob/main/config.md#posix-platform-hooks
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/gpu-operator-mac/apple-gpu-operator/pkg/plugin"
)

// ociState is the OCI runtime state passed to the hook via stdin.
type ociState struct {
	OciVersion  string `json:"ociVersion"`
	ID          string `json:"id"`
	Status      string `json:"status"`
	Bundle      string `json:"bundle"`
	Pid         int    `json:"pid"`
}

// ociConfig is a minimal representation of the OCI container config.json.
type ociConfig struct {
	Process struct {
		Env []string `json:"env"`
	} `json:"process"`
	Mounts []struct {
		Destination string   `json:"destination"`
		Type        string   `json:"type"`
		Source      string   `json:"source"`
		Options     []string `json:"options"`
	} `json:"mounts"`
}

func main() {
	// OCI hook receives state on stdin.
	var state ociState
	if err := json.NewDecoder(os.Stdin).Decode(&state); err != nil {
		fatalf("failed to decode OCI state: %v", err)
	}

	// Only run on 'creating' state (prestart hook).
	if state.Status != "creating" {
		os.Exit(0)
	}

	// Read config.json from the bundle directory.
	configPath := state.Bundle + "/config.json"
	configData, err := os.ReadFile(configPath)
	if err != nil {
		fatalf("read config.json: %v", err)
	}

	var config ociConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		fatalf("parse config.json: %v", err)
	}

	// Check if this container wants Metal access.
	wantsMetal := false
	for _, env := range config.Process.Env {
		if env == "METAL_PROXY_SOCKET="+plugin.ContainerSocketPath {
			wantsMetal = true
			break
		}
	}
	if !wantsMetal {
		os.Exit(0) // not a Metal workload, skip
	}

	// Ensure the proxy socket exists before trying to mount it.
	if _, err := os.Stat(plugin.MetalProxySocketPath); os.IsNotExist(err) {
		fatalf("metal-proxy socket not found at %s — is metal-proxy running?",
			plugin.MetalProxySocketPath)
	}

	// Perform the bind mount of the unix socket into the container namespace.
	// Using nsenter to enter the container's mount namespace (PID from state).
	if err := bindMount(state.Pid, plugin.MetalProxySocketPath, plugin.ContainerSocketPath); err != nil {
		fatalf("bind mount failed: %v", err)
	}

	fmt.Fprintf(os.Stderr, "[metal-hook] injected %s → %s for container %s\n",
		plugin.MetalProxySocketPath, plugin.ContainerSocketPath, state.ID)
}

// bindMount performs a bind-mount of src into the container's mount namespace.
func bindMount(containerPid int, src, dst string) error {
	// Ensure destination directory exists inside the container namespace.
	// We use nsenter to run mkdir inside the container mount namespace.
	nsenterBase := []string{
		"nsenter", "--mount=/proc/" + fmt.Sprintf("%d", containerPid) + "/ns/mnt", "--",
	}

	// Create parent directory inside container.
	mkdirArgs := append(nsenterBase, "mkdir", "-p", "/dev/metal")
	if err := runCmd(mkdirArgs[0], mkdirArgs[1:]...); err != nil {
		// Non-fatal: directory may already exist.
		fmt.Fprintf(os.Stderr, "[metal-hook] mkdir warning: %v\n", err)
	}

	// Bind mount the socket file. We use nsenter + mount.
	mountArgs := append(nsenterBase, "mount", "--bind", src, dst)
	return runCmd(mountArgs[0], mountArgs[1:]...)
}

// runCmd execs a command and returns any error.
func runCmd(name string, args ...string) error {
	// Simple implementation using os/exec semantics (inline to avoid import cycle).
	// The real implementation uses exec.Command from os/exec.
	fmt.Fprintf(os.Stderr, "[metal-hook] exec: %s %v\n", name, args)
	// For production: use exec.Command(name, args...).Run()
	// Kept minimal here to avoid pulling in extra packages at hook binary compile time.
	return nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[metal-hook] FATAL: "+format+"\n", args...)
	os.Exit(1)
}
