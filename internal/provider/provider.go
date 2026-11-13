package provider

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"

	"github.com/openeverest/provider-valkey/internal/common"
)

// Compile-time check that Provider implements the required interface.
var _ controller.ProviderInterface = (*Provider)(nil)

// Provider implements controller.ProviderInterface for the Valkey provider.
type Provider struct {
	controller.BaseProvider
}

// New creates a new Provider instance.
func New() *Provider {
	return &Provider{
		BaseProvider: controller.BaseProvider{
			ProviderName: common.ProviderName,
			SchemeFuncs: []func(*runtime.Scheme) error{
				valkeyv1alpha1.AddToScheme,
				// Sync lists MonitoringBindings and the conformance harness's
				// scheme has only the provider's types.
				monitoringv1alpha1.AddToScheme,
			},
			WatchConfigs: []controller.WatchConfig{
				controller.WatchOwned(&valkeyv1alpha1.ValkeyCluster{}),
				controller.WatchExternal(&valkeyv1alpha1.ValkeyNode{}, valkeyNodeHandler, managedValkeyNode),
			},
		},
	}
}

// Sync creates or updates the ValkeyCluster resource from the Instance spec.
func (p *Provider) Sync(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing instance", "name", c.Name())

	// Provision the self-signed TLS material before the cluster references it.
	if tlsEnabled(c) {
		if err := ensureTLSSecret(c); err != nil {
			return err
		}
		if err := ensureClientCertSecret(c); err != nil {
			return err
		}
	}

	// Provision the default user's password before the cluster references it.
	if err := ensureAuthSecret(c); err != nil {
		return err
	}

	spec, err := buildClusterSpec(c)
	if err != nil {
		return err
	}

	vc := &valkeyv1alpha1.ValkeyCluster{
		ObjectMeta: c.ObjectMeta(c.Name()),
		Spec:       spec,
	}
	if err := c.Apply(vc); err != nil {
		return err
	}

	if err := syncPrimaryService(c); err != nil {
		return err
	}

	// Releases before the monitoring contract created scrape resources;
	// remove them even while the cluster is still provisioning.
	return deleteLegacyScrapes(c)
}

// Cleanup deletes the ValkeyCluster resource. Owner references normally handle
// garbage collection, but the delete is issued explicitly to be safe. The
// scrape resources of earlier releases go too: the VMPodScrapes the
// VictoriaMetrics Operator converted from PodMonitors have no owner.
func (p *Provider) Cleanup(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up instance", "name", c.Name())

	vc := &valkeyv1alpha1.ValkeyCluster{ObjectMeta: c.ObjectMeta(c.Name())}
	if err := c.Delete(vc); err != nil {
		return err
	}
	return deleteLegacyScrapes(c)
}
