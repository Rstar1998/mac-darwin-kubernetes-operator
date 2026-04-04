// cmd/scheduler/main.go — entry point for the metal-scheduler-extender.
// Serves HTTP endpoints /filter and /prioritize consumed by kube-scheduler.
package main

import (
	"flag"
	"net/http"
	"os"

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

	log.Info("Listening", "addr", listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Error(err, "server error")
		os.Exit(1)
	}
}
