// cmd/exporter/main.go — entry point for the metal-exporter.
// Starts the Prometheus HTTP server and the powermetrics scrape loop.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	exporterpkg "github.com/gpu-operator-mac/apple-gpu-operator/pkg/exporter"
	"github.com/gpu-operator-mac/apple-gpu-operator/pkg/plugin"
	"github.com/gpu-operator-mac/apple-gpu-operator/pkg/version"
)

func main() {
	var listenAddr string
	var scrapeInterval time.Duration

	flag.StringVar(&listenAddr, "listen-address", ":9100", "Prometheus metrics listen address.")
	flag.DurationVar(&scrapeInterval, "scrape-interval", 15*time.Second, "powermetrics scrape interval.")
	flag.Parse()

	opts := zap.Options{Development: os.Getenv("DEBUG") == "true"}
	opts.BindFlags(flag.CommandLine)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	log := ctrl.Log.WithName("exporter")
	log.Info("Starting metal-exporter", "version", version.Version)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Error(nil, "NODE_NAME env var required")
		os.Exit(1)
	}

	// Discover chip variant for metric labels.
	chipVariant := "unknown"
	if info, err := plugin.DiscoverGPU(); err == nil {
		chipVariant = info.ChipVariant
	}

	// k8s client for node annotation.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Error(err, "in-cluster config failed")
		os.Exit(1)
	}
	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Error(err, "build k8s client failed")
		os.Exit(1)
	}

	// Prometheus registry.
	reg := prometheus.NewRegistry()
	exporterpkg.RegisterMetrics(reg)

	// Start scrape loop.
	exp := exporterpkg.NewExporter(log, nodeName, chipVariant, scrapeInterval, k8sClient)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := exp.Run(ctx); err != nil && err != context.Canceled {
			log.Error(err, "exporter scrape loop exited unexpectedly")
		}
	}()

	// HTTP server.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		log.Info("Serving metrics", "addr", listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "HTTP server error")
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	log.Info("Shutting down exporter")
}
