package provider

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
			},
			WatchConfigs: []controller.WatchConfig{
				controller.WatchOwned(&valkeyv1alpha1.ValkeyCluster{}),
			},
		},
	}
}

// Validate checks that the Instance spec is valid for the Valkey provider.
func (p *Provider) Validate(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Validating instance", "name", c.Name())

	components := c.Instance().Spec.Components

	// The engine component is required.
	engine, ok := components[common.ComponentEngine]
	if !ok {
		return fmt.Errorf("the %q component is required", common.ComponentEngine)
	}
	if engine.Replicas != nil && *engine.Replicas < 0 {
		return fmt.Errorf("engine replicas must not be negative")
	}

	// The cluster topology requires at least three shards.
	if c.Instance().GetTopologyType() == common.TopologyCluster {
		shards, err := resolveShards(c)
		if err != nil {
			return err
		}
		if shards < minClusterShards {
			return fmt.Errorf("the cluster topology requires at least %d shards, got %d", minClusterShards, shards)
		}
	}

	return nil
}

// Sync creates or updates the ValkeyCluster resource from the Instance spec.
func (p *Provider) Sync(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing instance", "name", c.Name())

	spec, err := buildClusterSpec(c)
	if err != nil {
		return err
	}

	vc := &valkeyv1alpha1.ValkeyCluster{
		ObjectMeta: c.ObjectMeta(c.Name()),
		Spec:       spec,
	}

	return c.Apply(vc)
}

// Status translates the ValkeyCluster status into the provider-runtime Status.
func (p *Provider) Status(c *controller.Context) (controller.Status, error) {
	l := log.FromContext(c.Context())
	l.Info("Computing status", "name", c.Name())

	vc := &valkeyv1alpha1.ValkeyCluster{}
	if err := c.Get(vc, c.Name()); err != nil {
		return controller.Provisioning("Waiting for ValkeyCluster"), nil
	}

	switch vc.Status.State {
	case valkeyv1alpha1.ClusterStateReady:
		return controller.ReadyWithConnectionDetails(buildConnectionDetails(c)), nil
	case valkeyv1alpha1.ClusterStateFailed:
		return controller.Failed(statusMessage(vc, "cluster failed")), nil
	case valkeyv1alpha1.ClusterStateDegraded:
		return controller.Provisioning(statusMessage(vc, "cluster is degraded")), nil
	default:
		return controller.Provisioning(statusMessage(vc, "cluster is being created")), nil
	}
}

// Cleanup deletes the ValkeyCluster resource. Owner references normally handle
// garbage collection, but the delete is issued explicitly to be safe.
func (p *Provider) Cleanup(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up instance", "name", c.Name())

	vc := &valkeyv1alpha1.ValkeyCluster{ObjectMeta: c.ObjectMeta(c.Name())}
	return c.Delete(vc)
}

// statusMessage returns the operator's status message, or a fallback when empty.
func statusMessage(vc *valkeyv1alpha1.ValkeyCluster, fallback string) string {
	if vc.Status.Message != "" {
		return vc.Status.Message
	}
	return fallback
}
