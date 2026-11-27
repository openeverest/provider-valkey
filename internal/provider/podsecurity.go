// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"fmt"
	"slices"

	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

const (
	// openShiftSecurityGroup is the API group that serves
	// SecurityContextConstraints. Only OpenShift (and OKD) serve it.
	openShiftSecurityGroup = "security.openshift.io"

	// valkeyUID is the valkey user of the official images. They declare no
	// USER and run as root, so runAsNonRoot needs a numeric UID.
	valkeyUID int64 = 999

	// valkeyServerContainer is the operator's name for the Valkey container;
	// the operator copies its security context to the server-init container.
	valkeyServerContainer = "server"
)

// DetectOpenShift reports whether the cluster is OpenShift.
//
// OpenShift's default restricted-v2 SCC gives each namespace its own UID/GID
// range and rejects pods that pin IDs outside it, so Valkey pods leave
// runAsUser, runAsGroup and fsGroup to the SCC there.
func DetectOpenShift(cfg *rest.Config) (bool, error) {
	client, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, fmt.Errorf("create discovery client: %w", err)
	}
	groups, err := client.ServerGroups()
	if err != nil {
		return false, fmt.Errorf("list API groups: %w", err)
	}
	return slices.ContainsFunc(groups.Groups, func(g metav1.APIGroup) bool {
		return g.Name == openShiftSecurityGroup
	}), nil
}

// applyPodSecurity makes the Valkey pods pass the restricted Pod Security
// Standard everywhere, and OpenShift's restricted-v2 SCC on OpenShift.
func applyPodSecurity(spec *valkeyv1alpha1.ValkeyClusterSpec, openShift bool) {
	pod := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if !openShift {
		pod.RunAsUser = ptr.To(valkeyUID)
		pod.RunAsGroup = ptr.To(valkeyUID)
		// Data written by earlier releases, which ran Valkey as root, is
		// handed over to the valkey group on the first start.
		pod.FSGroup = ptr.To(valkeyUID)
		pod.FSGroupChangePolicy = ptr.To(corev1.FSGroupChangeOnRootMismatch)
	}
	spec.PodSecurityContext = pod

	spec.Containers = []corev1.Container{{
		Name:            valkeyServerContainer,
		SecurityContext: restrictedContainerSecurityContext(),
	}}
	spec.Exporter.SecurityContext = restrictedContainerSecurityContext()
}

func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}
