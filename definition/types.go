// Package definition contains shared types used across the provider definition.
//
// +k8s:openapi-gen=true
package definition

// TopologyType identifies a Valkey deployment topology.
type TopologyType string

const (
	// TopologyTypeReplication is a single-shard deployment (one primary with
	// zero or more replicas).
	TopologyTypeReplication TopologyType = "replication"
	// TopologyTypeCluster is a multi-shard, sharded deployment.
	TopologyTypeCluster TopologyType = "cluster"
)
