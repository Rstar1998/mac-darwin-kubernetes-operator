// cmd/scheduler/main.go — entry point for the metal-scheduler-extender.
// Serves HTTP endpoints /filter and /prioritize consumed by kube-scheduler.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	schedulerpkg "github.com/gpu-operator-mac/apple-gpu-operator/pkg/scheduler"
	"github.com/gpu-operator-mac/apple-gpu-operator/pkg/version"
)

func main() {
	var listenAddr string
	flag.StringVar(&listenAddr, "listen-address", ":8888", "Scheduler extender listen address.")
	flag.Parse()

	opts := zap.Options{Development: os.Getenv("DEBUG") == "true"}
	opts.BindFlags(flag.CommandLine)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	log := ctrl.Log.WithName("scheduler-extender")
	log.Info("Starting metal-scheduler-extender", "version", version.Version)

	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Error(err, "in-cluster config failed")
		os.Exit(1)
	}
	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Error(err, "k8s client error")
		os.Exit(1)
	}

	ext := schedulerpkg.NewExtender(log, k8sClient)
	mux := http.NewServeMux()
	ext.RegisterHandlers(mux)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in background.
	go func() {
		log.Info("Listening", "addr", listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "server error")
			os.Exit(1)
		}
	}()

	// Graceful shutdown on SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	log.Info("Received shutdown signal", "signal", sig)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error(err, "HTTP server shutdown error")
	}
	log.Info("Scheduler extender stopped gracefully")
}
