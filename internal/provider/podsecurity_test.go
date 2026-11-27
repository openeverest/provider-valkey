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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	valkeyv1alpha1 "github.com/valkey-io/valkey-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestSyncPodSecurity(t *testing.T) {
	restricted := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	tests := []struct {
		name      string
		openShift bool
		wantPod   *corev1.PodSecurityContext
	}{
		{
			name: "kubernetes pins the valkey user",
			wantPod: &corev1.PodSecurityContext{
				RunAsNonRoot:        ptr.To(true),
				RunAsUser:           ptr.To(int64(999)),
				RunAsGroup:          ptr.To(int64(999)),
				FSGroup:             ptr.To(int64(999)),
				FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
				SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		},
		{
			name:      "openshift leaves the IDs to the SCC",
			openShift: true,
			wantPod: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, cl := newTestContext(t, newTestInstance("", true), interceptor.Funcs{})
			if err := New(Options{OpenShift: tt.openShift}).Sync(c); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			vc := &valkeyv1alpha1.ValkeyCluster{}
			if err := cl.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testInstance}, vc); err != nil {
				t.Fatalf("getting ValkeyCluster: %v", err)
			}

			if !equality.Semantic.DeepEqual(vc.Spec.PodSecurityContext, tt.wantPod) {
				t.Errorf("podSecurityContext = %+v, want %+v", vc.Spec.PodSecurityContext, tt.wantPod)
			}
			wantContainers := []corev1.Container{{Name: "server", SecurityContext: restricted}}
			if !equality.Semantic.DeepEqual(vc.Spec.Containers, wantContainers) {
				t.Errorf("containers = %+v, want %+v", vc.Spec.Containers, wantContainers)
			}
			if !equality.Semantic.DeepEqual(vc.Spec.Exporter.SecurityContext, restricted) {
				t.Errorf("exporter securityContext = %+v, want %+v", vc.Spec.Exporter.SecurityContext, restricted)
			}
		})
	}
}

// discoveryServer serves the legacy and named API group lists discovery reads.
func discoveryServer(t *testing.T, groups ...string) *rest.Config {
	t.Helper()

	list := metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}}
	for _, name := range groups {
		version := metav1.GroupVersionForDiscovery{GroupVersion: name + "/v1", Version: "v1"}
		list.Groups = append(list.Groups, metav1.APIGroup{
			Name: name, Versions: []metav1.GroupVersionForDiscovery{version}, PreferredVersion: version,
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			_ = json.NewEncoder(w).Encode(metav1.APIVersions{
				TypeMeta: metav1.TypeMeta{Kind: "APIVersions"}, Versions: []string{"v1"},
			})
		case "/apis":
			_ = json.NewEncoder(w).Encode(list)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return &rest.Config{Host: server.URL}
}

func TestDetectOpenShift(t *testing.T) {
	for _, tt := range []struct {
		groups []string
		want   bool
	}{
		{groups: []string{"apps", "security.openshift.io"}, want: true},
		{groups: []string{"apps", "core.openeverest.io"}, want: false},
	} {
		got, err := DetectOpenShift(discoveryServer(t, tt.groups...))
		if err != nil {
			t.Fatalf("DetectOpenShift(%v): %v", tt.groups, err)
		}
		if got != tt.want {
			t.Errorf("DetectOpenShift(%v) = %v, want %v", tt.groups, got, tt.want)
		}
	}
}
