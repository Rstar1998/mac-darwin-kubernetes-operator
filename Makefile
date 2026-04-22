# ─────────────────────────────────────────────────────────────────────────────
# Apple GPU Operator — Makefile
# ─────────────────────────────────────────────────────────────────────────────

MODULE   := github.com/gpu-operator-mac/apple-gpu-operator
BINDIR   := bin
IMAGE_PREFIX ?= ghcr.io/gpu-operator-mac

# Component versions — bump these to release a new version
VERSION  ?= 0.1.0
GOFLAGS  := -ldflags "-X $(MODULE)/pkg/version.Version=$(VERSION)"

# Proto tooling
PROTOC         ?= protoc
PROTOC_GEN_GO  ?= protoc-gen-go
PROTOC_GEN_GRPC ?= protoc-gen-go-grpc

.PHONY: all build test lint generate proto clean docker-build docker-build-local \
        helm-lint helm-package install-tools deploy-local undeploy-local

# ───── Default target ──────────────────────────────────────────────────────
all: generate build test

# ───── Build all Go binaries ──────────────────────────────────────────────
build:
	@echo "→ Building all binaries (native)"
	@mkdir -p $(BINDIR)
	go build $(GOFLAGS) -o $(BINDIR)/apple-gpu-operator    ./cmd/operator/...
	go build $(GOFLAGS) -o $(BINDIR)/metal-device-plugin   ./cmd/device-plugin/...
	go build $(GOFLAGS) -o $(BINDIR)/metal-exporter        ./cmd/exporter/...
	go build $(GOFLAGS) -o $(BINDIR)/metal-scheduler       ./cmd/scheduler/...
	go build $(GOFLAGS) -o $(BINDIR)/metal-hook            ./cmd/hook/...
	@echo "✓ Binaries written to $(BINDIR)/"

# ───── Cross-compile for Linux (Docker Desktop K8s runs Linux) ───────────
build-linux:
	@echo "→ Cross-compiling for linux/arm64"
	@mkdir -p $(BINDIR)/linux-arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BINDIR)/linux-arm64/apple-gpu-operator    ./cmd/operator/...
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BINDIR)/linux-arm64/metal-device-plugin   ./cmd/device-plugin/...
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BINDIR)/linux-arm64/metal-exporter        ./cmd/exporter/...
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BINDIR)/linux-arm64/metal-scheduler       ./cmd/scheduler/...
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BINDIR)/linux-arm64/metal-hook            ./cmd/hook/...
	@echo "✓ Linux binaries written to $(BINDIR)/linux-arm64/"

# ───── Run Go tests ───────────────────────────────────────────────────────
test:
	@echo "→ Running unit tests"
	go test ./... -v -count=1 -race -timeout 120s

# ───── Lint ───────────────────────────────────────────────────────────────
lint:
	@which golangci-lint > /dev/null 2>&1 || (echo "golangci-lint not found — run: make install-tools"; exit 1)
	golangci-lint run ./... --timeout 5m

# ───── Generate CRD manifests + proto stubs ───────────────────────────────
generate: proto
	@echo "→ Generating CRD deepcopy & manifests"
	go generate ./...

proto:
	@echo "→ Compiling metal.proto"
	@which $(PROTOC) > /dev/null 2>&1 || (echo "protoc not found — brew install protobuf"; exit 1)
	$(PROTOC) \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/metal.proto

# ───── Build Docker images (for CI / registry push) ─────────────────────
docker-build:
	@echo "→ Building Docker images (linux/arm64)"
	docker buildx build --platform linux/arm64 \
		-t $(IMAGE_PREFIX)/apple-gpu-operator:$(VERSION) \
		-f build/Dockerfile.operator .
	docker buildx build --platform linux/arm64 \
		-t $(IMAGE_PREFIX)/metal-device-plugin:$(VERSION) \
		-f build/Dockerfile.device-plugin .
	docker buildx build --platform linux/arm64 \
		-t $(IMAGE_PREFIX)/metal-exporter:$(VERSION) \
		-f build/Dockerfile.exporter .
	docker buildx build --platform linux/arm64 \
		-t $(IMAGE_PREFIX)/metal-scheduler:$(VERSION) \
		-f build/Dockerfile.scheduler .

# ───── Build Docker images (for local Docker Desktop K8s testing) ────────
docker-build-local: build-linux
	@echo "→ Building Docker images locally for Docker Desktop K8s"
	docker build -t apple-gpu-operator:local -f build/Dockerfile.operator .
	docker build -t metal-device-plugin:local -f build/Dockerfile.device-plugin .
	docker build -t metal-exporter:local -f build/Dockerfile.exporter .
	docker build -t metal-scheduler:local -f build/Dockerfile.scheduler .
	@echo "✓ Local images built — use 'make deploy-local' to deploy"

# ───── Deploy to local Docker Desktop K8s ────────────────────────────────
deploy-local: docker-build-local
	@echo "→ Installing CRDs"
	kubectl apply -f charts/apple-gpu-operator/crds/
	@echo "→ Installing operator via Helm"
	helm upgrade --install apple-gpu-operator charts/apple-gpu-operator \
		--namespace apple-gpu-system --create-namespace \
		--set operator.image=apple-gpu-operator:local \
		--set metalProxy.image=metal-proxy:local \
		--set devicePlugin.image=metal-device-plugin:local \
		--set exporter.image=metal-exporter:local \
		--set schedulerExtender.image=metal-scheduler:local \
		--set image.pullPolicy=Never \
		--wait --timeout 5m
	@echo "→ Applying sample AppleGPUCluster CR"
	kubectl apply -f config/samples/applegpucluster.yaml
	@echo "✓ Deployed! Run 'kubectl get pods -n apple-gpu-system' to check status"

# ───── Undeploy from local K8s ───────────────────────────────────────────
undeploy-local:
	helm uninstall apple-gpu-operator -n apple-gpu-system 2>/dev/null || true
	kubectl delete -f charts/apple-gpu-operator/crds/ 2>/dev/null || true
	kubectl delete namespace apple-gpu-system 2>/dev/null || true
	@echo "✓ Undeployed"

# ───── Helm ───────────────────────────────────────────────────────────────
helm-lint:
	helm lint charts/apple-gpu-operator

helm-package:
	helm package charts/apple-gpu-operator --destination dist/

# ───── Swift metal-proxy ─────────────────────────────────────────────────
metal-proxy-build:
	@echo "→ Building metal-proxy (Swift)"
	cd metal-proxy && swift build -c release

metal-proxy-test:
	cd metal-proxy && swift test

# ───── Install dev tooling ───────────────────────────────────────────────
install-tools:
	@echo "→ Installing dev tooling"
	brew install protobuf golangci-lint helm
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# ───── Cleanup ───────────────────────────────────────────────────────────
clean:
	rm -rf $(BINDIR) dist/
	find . -name "*.pb.go" -delete
