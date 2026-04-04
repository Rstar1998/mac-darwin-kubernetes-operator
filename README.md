# Apple GPU Operator

> **Enable GPU-accelerated container workloads on Mac Mini M-series clusters**  
> A production-grade Kubernetes operator modelled after NVIDIA's GPU Operator — built for Apple Silicon.

[![CI](https://github.com/gpu-operator-mac/apple-gpu-operator/actions/workflows/ci.yml/badge.svg)](https://github.com/gpu-operator-mac/apple-gpu-operator/actions/workflows/ci.yml)

---

## Overview

NVIDIA's GPU Operator makes it trivial to run GPU workloads in Kubernetes by automating driver, runtime, device-plugin, and monitoring lifecycle. The Apple GPU Operator does the same for a cluster of **Mac Mini M-series nodes**, enabling any container to access Apple Silicon GPU and ANE (Apple Neural Engine) compute through a clean Kubernetes API.

```yaml
resources:
  limits:
    apple.com/gpu: "1"   # ← that's it
```

### The Architecture (Metal Compute Proxy)

Since macOS does not support Linux-style GPU device node passthrough, we use a **Metal Compute Proxy** pattern:

```
Container ──(Unix Socket)──▶ metal-proxy daemon ──▶ MTLCommandQueue ──▶ Apple GPU
```

The device plugin injects `/dev/metal/proxy.sock` into containers via `METAL_PROXY_SOCKET`. Workloads call the proxy's gRPC API to submit MLX, PyTorch MPS, or raw Metal compute jobs.

---

## Components

| Component | Language | Role |
|---|---|---|
| `metal-proxy` | Swift | Host daemon; owns `MTLCommandQueue` pool, dispatches Metal jobs |
| `metal-device-plugin` | Go | Advertises `apple.com/gpu` slots to kubelet, injects socket |
| `apple-gpu-operator` | Go | CRD controller managing DaemonSet lifecycle |
| `metal-scheduler-extender` | Go | Filter + prioritize: thermal state + GPU utilization |
| `metal-exporter` | Go | Prometheus metrics from `powermetrics` |
| `metal-hook` | Go | OCI prestart hook for socket bind-mount |

---

## Quick Start

### 1. Prerequisites

- 2+ Mac Mini M-series nodes (M2 or later recommended)
- macOS 14 Sonoma or later on all nodes
- SSH key-based access between nodes
- `helm` and `kubectl` installed locally

### 2. Bootstrap Cluster

```bash
git clone https://github.com/gpu-operator-mac/apple-gpu-operator
cd apple-gpu-operator

SERVER_IP=192.168.1.10 \
AGENT_IPS="192.168.1.11 192.168.1.12" \
bash scripts/bootstrap-cluster.sh
```

This script installs **k3s**, **cert-manager**, **Node Feature Discovery**, and the operator Helm chart automatically.

### 3. Verify GPU Resources

```bash
kubectl get nodes -o custom-columns=\
"NAME:.metadata.name,GPU:.status.allocatable.apple\.com/gpu,CHIP:.metadata.labels.apple\.com/chip-variant"
```

Expected output:
```
NAME              GPU   CHIP
mac-mini-1        4     m3-max
mac-mini-2        4     m3-max
```

### 4. Run an Inference Job

```bash
kubectl apply -f config/samples/mlx-inference-job.yaml
kubectl logs -l app=mlx-inference-demo --follow
```

---

## Manual Helm Install

```bash
helm install apple-gpu-operator charts/apple-gpu-operator \
  --namespace apple-gpu-system --create-namespace \
  --set coresPerSlot=10 \
  --set exporter.enabled=true \
  --set schedulerExtender.enabled=true
```

Then apply your cluster config:

```bash
kubectl apply -f config/samples/applegpucluster.yaml
```

---

## GPU Slot Model

Apple Silicon GPUs don't expose individually addressable cores like NVIDIA MIG. Instead, the operator uses **logical slots**.

By default, **1 Apple Silicon Node = 1 `apple.com/gpu` slot**, representing the entire physical GPU. A pod requesting `apple.com/gpu: 1` gets time-shared access to the whole GPU via the metal-proxy command queue.

For advanced users on larger nodes (e.g., M3 Max), you can configure fractional slicing by setting `coresPerSlot`:

```
Physical GPU Cores ÷ coresPerSlot = apple.com/gpu slots
     40 cores      ÷     10       =       4 slots
```

If used, workloads request `apple.com/gpu: N` and get access to N×coresPerSlot proportional compute.

---

## Observability

Metrics endpoint: `http://<node>:9100/metrics`

| Metric | Description |
|---|---|
| `apple_gpu_utilization_percent` | GPU engine utilization (0–100) |
| `apple_gpu_power_watts` | GPU power draw in watts |
| `apple_ane_power_milliwatts` | Apple Neural Engine power (proxy for utilization) |
| `apple_node_thermal_state` | 0=Nominal 1=Fair 2=Serious 3=Critical |
| `apple_cpu_package_temp_celsius` | CPU package temperature |
| `apple_gpu_slots_total` | Total advertised GPU slots |
| `apple_gpu_slots_allocated` | Currently allocated slots |

---

## Development

```bash
# Build all Go binaries
make build

# Run tests
make test

# Build Swift metal-proxy
make metal-proxy-build

# Lint
make lint

# Generate proto stubs (requires protoc)
make proto
```

---

## Reference Projects

This operator draws architectural inspiration from:
- **[NVIDIA GPU Operator](https://github.com/NVIDIA/gpu-operator)** — overall structure
- **[Intel Device Plugins Operator](https://github.com/intel/intel-device-plugins-for-kubernetes)** — CRD lifecycle + NFD integration
- **[libkrun/krunkit](https://github.com/containers/libkrun)** — Vulkan→Metal GPU forwarding validates the bridge pattern
- **[Kubernetes Device Plugin API](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/)** — device plugin spec

---

## License

Apache 2.0
