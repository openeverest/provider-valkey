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
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-valkey/internal/common"
)

var _ controller.ReferenceValidator = (*Provider)(nil)

var componentsPath = field.NewPath("spec", "components")

// Validate checks the rules about the Instance alone. The runtime runs it once
// the Instance matches the topologies, components, versions and parameter
// schemas the Provider declares.
func (p *Provider) Validate(in controller.ValidationInput) controller.ValidationResult {
	instance := in.Instance
	enginePath := componentsPath.Key(common.ComponentEngine)

	engine, ok := instance.Spec.Components[common.ComponentEngine]
	if !ok {
		return controller.ValidationResult{Errors: field.ErrorList{field.Required(enginePath, "")}}
	}

	var errs field.ErrorList
	if engine.Replicas != nil && *engine.Replicas < 0 {
		errs = append(errs, field.Invalid(enginePath.Child("replicas"), *engine.Replicas, "must not be negative"))
	} else {
		errs = append(errs, validateClusterName(instance, ptr.Deref(engine.Replicas, 0))...)
	}
	errs = append(errs, validateTLS(&in.Provider.Spec, instance)...)
	errs = append(errs, validateTLSTransition(instance, in.Old)...)

	return controller.ValidationResult{Errors: errs}
}

// ValidateReferences checks the rules that read the cluster: the running
// ValkeyCluster and the instance's CA key.
func (p *Provider) ValidateReferences(c *controller.Context) (field.ErrorList, error) {
	return validateTLSReferences(c)
}
