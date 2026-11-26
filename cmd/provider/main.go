// package main implements the provider-valkey provider.
package main

import (
	"flag"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openeverest/openeverest/v2/provider-runtime/reconciler"

	"github.com/openeverest/provider-valkey/internal/provider"
)

// main is the entry point for the provider.
func main() {
	var admission bool
	flag.BoolVar(&admission, "admission", true, "Serve the Instance admission webhook, with the serving certificate the chart mounts.")
	flag.Parse()

	l := ctrl.Log.WithName("setup")
	ctx := ctrl.SetupSignalHandler()

	// reconciler.New installs the logger, so failures before it go to stderr.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to load kubeconfig: %v\n", err)
		os.Exit(1)
	}
	openShift, err := provider.DetectOpenShift(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to detect the cluster platform: %v\n", err)
		os.Exit(1)
	}

	p := provider.New(provider.Options{OpenShift: openShift})

	opts := []reconciler.ReconcilerOption{
		// Serve the health and readiness probes.
		reconciler.WithServer(reconciler.ServerConfig{Port: 8082}),
	}
	if admission {
		opts = append(opts, reconciler.WithAdmission())
	}

	r, err := reconciler.New(ctx, p, opts...)
	if err != nil {
		l.Error(err, "unable to create reconciler")
		os.Exit(1)
	}
	l.Info("detected cluster platform", "openShift", openShift)

	if err := r.Start(ctx); err != nil {
		l.Error(err, "unable to start reconciler")
		os.Exit(1)
	}
}
