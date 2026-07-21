// Package replication contains custom spec types for the replication topology.
//
// Add fields to ReplicationTopologyConfig and reference it via configSchema in
// topology.yaml when this topology needs custom configuration.
//
// +k8s:openapi-gen=true
package replication

// ReplicationTopologyConfig defines configuration for the replication topology.
// Add fields here when the replication topology needs custom configuration
// beyond what the base Instance spec provides.
//
// Example:
//   type ReplicationTopologyConfig struct {
//       NumShards int32 `json:"numShards,omitempty"`
//   }
//
// Then reference it in topology.yaml:
//   config:
//     configSchema: ReplicationTopologyConfig
type ReplicationTopologyConfig struct{}
