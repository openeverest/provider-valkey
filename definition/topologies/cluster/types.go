// Package cluster contains custom spec types for the cluster topology.
//
// Add fields to ClusterTopologyConfig and reference it via configSchema in
// topology.yaml when this topology needs custom configuration.
//
// +k8s:openapi-gen=true
package cluster

// ClusterTopologyConfig defines configuration for the cluster topology.
type ClusterTopologyConfig struct {
	// NumShards is the number of shard groups in the Valkey cluster. Each shard
	// owns a subset of the 16384 hash slots. A Valkey cluster requires at least
	// three primaries, so the minimum is 3.
	// +kubebuilder:validation:Minimum=3
	// +optional
	NumShards int32 `json:"numShards,omitempty"`
}
