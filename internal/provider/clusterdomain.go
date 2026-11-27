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
	"strings"

	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// clusterDomain is the DNS suffix kubelet publishes Service DNS under
// (kubelet --cluster-domain). It is a property of the Kubernetes cluster, not
// of an Instance, so it is set once at startup with SetClusterDomain.
var clusterDomain = valkeyv1alpha1.DefaultClusterDomain

// SetClusterDomain sets the DNS suffix of the Service names the provider
// connects clients to, issues certificates for and passes to valkey-operator.
// An empty domain selects cluster.local; a trailing dot is dropped.
func SetClusterDomain(domain string) error {
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" {
		domain = valkeyv1alpha1.DefaultClusterDomain
	}
	if errs := validation.IsDNS1123Subdomain(domain); len(errs) > 0 {
		return fmt.Errorf("invalid cluster domain %q: %s", domain, strings.Join(errs, "; "))
	}
	clusterDomain = domain
	return nil
}

// serviceFQDN is the cluster DNS name of a Service.
func serviceFQDN(service, namespace string) string {
	return fmt.Sprintf("%s.%s.svc.%s", service, namespace, clusterDomain)
}
