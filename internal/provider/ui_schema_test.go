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
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/openeverest/provider-valkey/internal/common"
)

// uiFields returns every component of the generated UI schema, which holds
// the forms of all topologies, that writes a path.
func uiFields(t *testing.T) []map[string]any {
	t.Helper()
	spec := generatedProviderSpec(t)
	require.NotNil(t, spec.UISchema)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(spec.UISchema.Raw, &schema))
	require.Len(t, schema, len(spec.Topologies))

	var fields []map[string]any
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if _, ok := n["path"].(string); ok {
				if _, ok := n["uiType"]; ok {
					fields = append(fields, n)
				}
			}
			for _, v := range n {
				walk(v)
			}
		case []any:
			for _, v := range n {
				walk(v)
			}
		}
	}
	walk(schema)
	require.NotEmpty(t, fields)
	return fields
}

// resourceField matches the paths of CPU, memory and disk quantities.
var resourceField = regexp.MustCompile(`^spec\.components\.(\w+)\.(?:resources\.(?:limits|requests)\.(cpu|memory)|storage\.(size))$`)

// Resource fields are quantity fields: the form holds a number in the field's
// unit and writes a quantity string, so fractional CPU ("0.5") reaches the
// API as a string it accepts. The engine's CPU is in cores, its memory and
// disk in Gi; the exporter sidecar's CPU is in millicores and its memory in
// Mi. The default and the smallest value a field offers must be valid
// quantities.
func TestUIResourceFieldsAreQuantities(t *testing.T) {
	units := map[string]map[string]string{
		common.ComponentEngine:     {"cpu": "", "memory": "Gi", "size": "Gi"},
		common.ComponentMonitoring: {"cpu": "m", "memory": "Mi"},
	}
	unitLabels := map[string]map[string]string{
		common.ComponentEngine:     {"cpu": "CPU"},
		common.ComponentMonitoring: {"cpu": "mCPU"},
	}

	var checked int
	for _, field := range uiFields(t) {
		path := field["path"].(string)
		m := resourceField.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		checked++
		component, kind := m[1], m[2]+m[3]

		require.Equal(t, "quantity", field["uiType"], path)
		params, _ := field["fieldParams"].(map[string]any)
		validation, _ := field["validation"].(map[string]any)
		assert.NotContains(t, params, "badge", path)
		assert.NotContains(t, params, "badgeToApi", path)

		want, ok := units[component][kind]
		require.True(t, ok, "%s: no unit expected for this field", path)
		unit, _ := params["unit"].(string)
		assert.Equal(t, want, unit, path)
		if label, ok := unitLabels[component][kind]; ok {
			assert.Equal(t, label, params["unitLabel"], path)
		}

		for _, v := range []any{params["defaultValue"], validation["min"]} {
			n, ok := v.(float64)
			require.True(t, ok, "%s needs a numeric defaultValue and min", path)
			q, err := resource.ParseQuantity(strconv.FormatFloat(n, 'f', -1, 64) + unit)
			if assert.NoError(t, err, "%s writes %v%s", path, n, unit) {
				assert.Positive(t, q.Sign(), "%s writes %v%s", path, n, unit)
			}
		}
	}
	// CPU, memory and disk of the engine and CPU and memory of the exporter,
	// in both topologies.
	assert.Equal(t, 10, checked)
}

var celPath = regexp.MustCompile(`(?:original\.)?(spec(?:\.\w+)+)`)

// setPath stores value at a dotted path, creating the maps on the way.
func setPath(obj map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := obj[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			obj[p] = next
		}
		obj = next
	}
	obj[parts[len(parts)-1]] = value
}

// Edit rules compare a field with its original value. Quantity fields read
// both as numbers in the field's unit, whole (10) or not (10.5); a value that
// reaches CEL as a string ("10", as number fields with a badge were read)
// must not fail with "no such overload" either, which would keep Save
// disabled.
func TestUIEditRulesAcceptStringAndNumberValues(t *testing.T) {
	env, err := cel.NewEnv(
		cel.Variable("spec", cel.DynType),
		cel.Variable("original", cel.DynType),
	)
	require.NoError(t, err)

	var rules []string
	for _, field := range uiFields(t) {
		validation, _ := field["validation"].(map[string]any)
		modes, _ := validation["modes"].(map[string]any)
		edit, _ := modes["edit"].(map[string]any)
		for _, list := range []any{validation["celExpressions"], edit["celExpressions"]} {
			exprs, _ := list.([]any)
			for _, e := range exprs {
				if expr, _ := e.(map[string]any)["celExpr"].(string); strings.Contains(expr, "original.") {
					rules = append(rules, expr)
				}
			}
		}
	}
	require.NotEmpty(t, rules)

	eval := func(expr string, form, original any) (any, error) {
		ast, iss := env.Compile(expr)
		require.NoError(t, iss.Err(), expr)
		prg, err := env.Program(ast)
		require.NoError(t, err)
		vars := map[string]any{"original": map[string]any{}}
		for _, m := range celPath.FindAllStringSubmatch(expr, -1) {
			setPath(vars, m[1], form)
			setPath(vars["original"].(map[string]any), m[1], original)
		}
		out, _, err := prg.Eval(vars)
		if err != nil {
			return nil, err
		}
		return out.Value(), nil
	}

	for _, expr := range rules {
		for _, tc := range []struct {
			form, original any
			want           bool
		}{
			{"10", 10, true},
			{10, 10, true},
			{int64(11), int64(10), true},
			{10.5, int64(10), true},
			{"9", 10, false},
			{9, 10, false},
			{9.5, int64(10), false},
		} {
			got, err := eval(expr, tc.form, tc.original)
			if assert.NoError(t, err, "%s with %#v vs %#v", expr, tc.form, tc.original) {
				assert.Equal(t, tc.want, got, "%s with %#v vs %#v", expr, tc.form, tc.original)
			}
		}
	}
}
