// package main implements the provider-valkey provider.
package main

import (
	"flag"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openeverest/openeverest/v2/provider-runtime/reconciler"

	"github.com/openeverest/provider-valkey/internal/provider"
)

// main is the entry point for the provider.
func main() {
	var admission bool
	var clusterDomain string
	flag.BoolVar(&admission, "admission", true, "Serve the Instance admission webhook, with the serving certificate the chart mounts.")
	flag.StringVar(&clusterDomain, "cluster-domain", envOr("CLUSTER_DOMAIN", "cluster.local"),
		"DNS suffix of Service names in the Kubernetes cluster (kubelet --cluster-domain). Defaults to $CLUSTER_DOMAIN.")
	flag.Parse()

	l := ctrl.Log.WithName("setup")
	ctx := ctrl.SetupSignalHandler()

	if err := provider.SetClusterDomain(clusterDomain); err != nil {
		l.Error(err, "invalid --cluster-domain")
		os.Exit(1)
	}

	p := provider.New()

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

	if err := r.Start(ctx); err != nil {
		l.Error(err, "unable to start reconciler")
		os.Exit(1)
	}
}

// envOr returns the value of the environment variable key, or def when unset.
func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
