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

.PHONY: all build test lint generate proto clean docker-build helm-lint \
        helm-package install-tools

# ───── Default target ──────────────────────────────────────────────────────
all: generate build test

# ───── Build all Go binaries ──────────────────────────────────────────────
build:
	@echo "→ Building all binaries"
	@mkdir -p $(BINDIR)
	go build $(GOFLAGS) -o $(BINDIR)/apple-gpu-operator    ./cmd/operator/...
	go build $(GOFLAGS) -o $(BINDIR)/metal-device-plugin   ./cmd/device-plugin/...
	go build $(GOFLAGS) -o $(BINDIR)/metal-exporter        ./cmd/exporter/...
	go build $(GOFLAGS) -o $(BINDIR)/metal-scheduler       ./cmd/scheduler/...
	go build $(GOFLAGS) -o $(BINDIR)/metal-hook            ./cmd/hook/...
	@echo "✓ Binaries written to $(BINDIR)/"

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

# ───── Build + tag Docker images ─────────────────────────────────────────
docker-build:
	@echo "→ Building Docker images (darwin/arm64)"
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
