#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# bootstrap-cluster.sh — Bootstrap a k3s Kubernetes cluster across Mac Mini
# M-series nodes and install the Apple GPU Operator.
#
# Usage:
#   SERVER_IP=192.168.1.10 AGENT_IPS="192.168.1.11 192.168.1.12" \
#     bash scripts/bootstrap-cluster.sh
#
# Requirements on each node:
#   - macOS 14+ (Sonoma)
#   - SSH key-based access from this machine
#   - brew installed
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

SERVER_IP="${SERVER_IP:-}"
AGENT_IPS="${AGENT_IPS:-}"
SSH_USER="${SSH_USER:-$(whoami)}"
K3S_VERSION="${K3S_VERSION:-v1.30.2+k3s1}"
NAMESPACE="apple-gpu-system"
OPERATOR_CHART="./charts/apple-gpu-operator"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()    { echo -e "${GREEN}[INFO]${NC} $*"; }
warning() { echo -e "${YELLOW}[WARN]${NC} $*"; }
die()     { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

[[ -z "$SERVER_IP" ]] && die "SERVER_IP is required. Example: SERVER_IP=192.168.1.10"

# ─── Step 1: Install k3s server ──────────────────────────────────────────────
info "Installing k3s server on $SERVER_IP ..."
ssh "${SSH_USER}@${SERVER_IP}" bash <<EOF
  set -e
  curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="${K3S_VERSION}" sh -s - server \
    --disable traefik \
    --disable servicelb \
    --kubelet-arg="node-labels=apple.com/chip-family=m-series"
  echo "k3s server installed"
EOF

# Grab the node token for agents.
K3S_TOKEN=$(ssh "${SSH_USER}@${SERVER_IP}" "sudo cat /var/lib/rancher/k3s/server/node-token")
info "k3s token retrieved"

# Copy kubeconfig locally.
mkdir -p ~/.kube
ssh "${SSH_USER}@${SERVER_IP}" "sudo cat /etc/rancher/k3s/k3s.yaml" \
  | sed "s/127.0.0.1/${SERVER_IP}/g" > ~/.kube/config-mac-mini-cluster
export KUBECONFIG=~/.kube/config-mac-mini-cluster
info "kubeconfig saved to ~/.kube/config-mac-mini-cluster"

# ─── Step 2: Install k3s agents ──────────────────────────────────────────────
for AGENT_IP in $AGENT_IPS; do
  info "Installing k3s agent on $AGENT_IP ..."
  ssh "${SSH_USER}@${AGENT_IP}" bash <<EOF
    set -e
    curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="${K3S_VERSION}" \
      K3S_URL="https://${SERVER_IP}:6443" \
      K3S_TOKEN="${K3S_TOKEN}" \
      sh -s - agent \
        --kubelet-arg="node-labels=apple.com/chip-family=m-series"
    echo "k3s agent installed on ${AGENT_IP}"
EOF
done

# ─── Step 3: Wait for all nodes ready ────────────────────────────────────────
info "Waiting for all nodes to be Ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=120s
kubectl get nodes -o wide

# ─── Step 4: Install cert-manager ─────────────────────────────────────────────
info "Installing cert-manager ..."
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl wait --for=condition=Available deployment/cert-manager \
  -n cert-manager --timeout=120s
info "cert-manager ready"

# ─── Step 5: Install Node Feature Discovery (NFD) ────────────────────────────
info "Installing Node Feature Discovery ..."
kubectl apply -k https://github.com/kubernetes-sigs/node-feature-discovery/deployment/overlays/default?ref=v0.16.0
kubectl wait --for=condition=Available deployment/nfd-master \
  -n node-feature-discovery --timeout=120s
info "NFD ready"

# ─── Step 6: Install Apple GPU Operator ──────────────────────────────────────
info "Installing Apple GPU Operator ..."
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install apple-gpu-operator "${OPERATOR_CHART}" \
  --namespace "${NAMESPACE}" \
  --set monitoring.serviceMonitor.enabled=false \
  --wait --timeout 5m
info "Apple GPU Operator installed"

# ─── Step 7: Apply sample AppleGPUCluster CR ─────────────────────────────────
info "Applying AppleGPUCluster CR ..."
kubectl apply -f config/samples/applegpucluster.yaml

# ─── Step 8: Verify GPU resources ────────────────────────────────────────────
info "Waiting for device plugin to register ..."
sleep 30
echo ""
echo "=== Node GPU Resources ==="
kubectl get nodes -o custom-columns=\
"NAME:.metadata.name,\
GPU-SLOTS:.status.allocatable.apple\.com/gpu,\
CHIP:.metadata.labels.apple\.com/chip-variant"
echo ""
echo "=== DaemonSet Status ==="
kubectl get daemonset -n "${NAMESPACE}"
echo ""
info "✓ Bootstrap complete! GPU resources should be visible above."
info "To test: kubectl apply -f config/samples/mlx-inference-job.yaml"
