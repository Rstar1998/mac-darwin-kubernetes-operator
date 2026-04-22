// cmd/device-plugin/main.go — entry point for the metal-device-plugin.
// Discovers the Apple Silicon GPU, registers with kubelet, and serves the
// Device Plugin gRPC API.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/gpu-operator-mac/apple-gpu-operator/pkg/plugin"
	"github.com/gpu-operator-mac/apple-gpu-operator/pkg/version"
)

func main() {
	var coresPerSlot int
	flag.IntVar(&coresPerSlot, "cores-per-slot", plugin.DefaultCoresPerSlot,
		"Number of physical GPU cores that map to one apple.com/gpu logical slot.")
	flag.Parse()

	opts := zap.Options{Development: os.Getenv("DEBUG") == "true"}
	opts.BindFlags(flag.CommandLine)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	log := ctrl.Log.WithName("device-plugin")

	// Override coresPerSlot from env (set by the operator DaemonSet).
	// Env var takes precedence over the --cores-per-slot flag.
	if envVal := os.Getenv("CORES_PER_SLOT"); envVal != "" {
		parsed, err := strconv.Atoi(envVal)
		if err != nil {
			log.Error(err, "invalid CORES_PER_SLOT env var, using flag value", "envVal", envVal)
		} else {
			coresPerSlot = parsed
		}
	}

	log.Info("Starting metal-device-plugin", "version", version.Version, "coresPerSlot", coresPerSlot)

	// Discover GPU.
	gpuInfo, err := plugin.DiscoverGPU()
	if err != nil {
		log.Error(err, "GPU discovery failed — is this an Apple Silicon node?")
		os.Exit(1)
	}
	log.Info("GPU discovered",
		"chip", gpuInfo.ChipModel,
		"cores", gpuInfo.GPUCores,
		"variant", gpuInfo.ChipVariant,
	)

	// Build k8s client from in-cluster config.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Error(err, "cannot load in-cluster k8s config")
		os.Exit(1)
	}
	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Error(err, "cannot create k8s client")
		os.Exit(1)
	}

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Error(nil, "NODE_NAME env var is required (set via Downward API)")
		os.Exit(1)
	}

	dp := plugin.NewMetalDevicePlugin(log, gpuInfo, coresPerSlot, k8sClient, nodeName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start device plugin server.
	if err := dp.Start(ctx); err != nil {
		log.Error(err, "failed to start device plugin server")
		os.Exit(1)
	}
	defer dp.Stop()

	log.Info("metal-device-plugin registered with kubelet",
		"resource", plugin.ResourceName,
		"slots", dp.TotalSlots(),
	)

	// Wait for termination signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	log.Info("received shutdown signal", "signal", sig)
}
