package provider

// Run `make manifests` to regenerate config/rbac/role.yaml from these markers.
// This file contains kubebuilder RBAC markers for controller-gen.
// See: https://book.kubebuilder.io/reference/markers/rbac

// Base RBAC (required by all providers):
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=core.openeverest.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// =============================================================================
// PROVIDER-SPECIFIC RBAC — permissions for the valkey-operator resources
// this provider creates and reads.
// =============================================================================
// +kubebuilder:rbac:groups=valkey.io,resources=valkeyclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=valkey.io,resources=valkeyclusters/status,verbs=get
// +kubebuilder:rbac:groups=valkey.io,resources=valkeyclusters/finalizers,verbs=update

// Core resources used to read connection endpoints and pass through config.
// +kubebuilder:rbac:groups="",resources=services;secrets;configmaps,verbs=get;list;watch
