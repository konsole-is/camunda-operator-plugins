/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pluginkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const sizeSchema = `{"type":"object","properties":{"size":{"type":"integer","minimum":1}},"required":["size"]}`

const templateSchema = `{"type":"object","properties":{` +
	`"policy":{"type":"string","x-fleet-template":true},"size":{"type":"integer"}}}`

func rawJSON(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

func TestValidateParametersAcceptsAValidValue(t *testing.T) {
	assert.NoError(t, ValidateParameters(t.Context(), rawJSON(sizeSchema), rawJSON(`{"size":3}`)))
}

func TestValidateParametersNamesTheField(t *testing.T) {
	err := ValidateParameters(t.Context(), rawJSON(sizeSchema), rawJSON(`{"size":0}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.parameters.size")

	err = ValidateParameters(t.Context(), rawJSON(sizeSchema), rawJSON(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.parameters.size")
	assert.Contains(t, err.Error(), "Required")
}

func TestValidateParametersAcceptsAnythingWithoutASchema(t *testing.T) {
	assert.NoError(t, ValidateParameters(t.Context(), nil, rawJSON(`{"x":1}`)))
}

func TestValidateParametersChecksAbsentParametersAsAnEmptyObject(t *testing.T) {
	err := ValidateParameters(t.Context(), rawJSON(sizeSchema), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.parameters.size")
}

func TestValidateParametersRefusesASchemaThatIsNotJSON(t *testing.T) {
	assert.Error(t, ValidateParameters(t.Context(), rawJSON(`{"type":`), rawJSON(`{}`)))
}

func TestValidateParametersRunsTheValidationRulesOfTheSchema(t *testing.T) {
	schema := rawJSON(`{
  "type": "object",
  "properties": {
    "min": {"type": "integer"},
    "max": {"type": "integer"},
    "name": {
      "type": "string",
      "x-kubernetes-validations": [{"rule": "self.startsWith('team-')", "message": "name must start with team-"}]
    }
  },
  "x-kubernetes-validations": [
    {"rule": "!has(self.min) || !has(self.max) || self.min <= self.max", "message": "min must not be above max"},
    {"rule": "!has(self.max) || self.max < 100"}
  ]
}`)

	tests := []struct {
		name       string
		parameters string
		want       []string // each in the error. nil means pass
	}{
		{"parameters that keep the rules pass", `{"min":1,"max":2,"name":"team-a"}`, nil},
		{"a rule with a message", `{"min":3,"max":2}`, []string{"spec.parameters", "min must not be above max"}},
		{"a rule without a message names the rule", `{"max":200}`, []string{"self.max < 100"}},
		{
			"a rule of a property names the property",
			`{"name":"a"}`,
			[]string{"spec.parameters.name", "name must start with team-"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateParameters(t.Context(), schema, rawJSON(tt.parameters))
			if tt.want == nil {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			for _, w := range tt.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestValidateParametersChecksTheListsAndEmbeddedResources(t *testing.T) {
	const schema = `{"type":"object","properties":{` +
		`"tags":{"type":"array","x-kubernetes-list-type":"set","items":{"type":"string"}},` +
		`"ports":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["name"],` +
		`"items":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}},` +
		`"object":{"type":"object","x-kubernetes-embedded-resource":true,"x-kubernetes-preserve-unknown-fields":true}}}`
	tests := []struct {
		name       string
		parameters string
		message    string
	}{
		{"valid", `{"tags":["a","b"],"ports":[{"name":"a"},{"name":"b"}],` +
			`"object":{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"a"}}}`, ""},
		{"duplicate set element", `{"tags":["a","a"]}`, `spec.parameters.tags[1]: Duplicate value: "a"`},
		{"duplicate map key", `{"ports":[{"name":"a"},{"name":"a"}]}`, "spec.parameters.ports[1]: Duplicate value"},
		{
			"embedded resource without a kind",
			`{"object":{"apiVersion":"v1"}}`,
			"spec.parameters.object.kind: Required value",
		},
		{
			"embedded resource with a name that is not a path segment",
			`{"object":{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"a/b"}}}`,
			"spec.parameters.object.metadata.name: Invalid value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateParameters(t.Context(), rawJSON(schema), rawJSON(tt.parameters))

			if tt.message == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.message)
		})
	}
}

func TestValidateParametersChecksNullParametersAsAnEmptyObject(t *testing.T) {
	err := ValidateParameters(t.Context(), rawJSON(sizeSchema), rawJSON(`null`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.parameters.size")
}

func TestValidateParametersRunsTheRulesOnTheValueBeforeItRenders(t *testing.T) {
	schema := `{"type":"object","properties":{"policy":{"type":"string","x-fleet-template":true,` +
		`"x-kubernetes-validations":[{"rule":"!self.contains('{{')","message":"policy must not be a template"}]}}}`

	err := ValidateParameters(t.Context(), rawJSON(schema), rawJSON(`{"policy":"{{ .namespace }}"}`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy must not be a template")
}

func TestValidateParametersReportsARuleThatDoesNotCompile(t *testing.T) {
	schema := `{"type":"object","properties":{"n":{"type":"integer","x-kubernetes-validations":[{"rule":"self >"}]}}}`

	err := ValidateParameters(t.Context(), rawJSON(schema), rawJSON(`{"n":1}`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.parameters.n")
	assert.Contains(t, err.Error(), "rule compile error")
}

func TestTemplateParametersFindsTheMarkedProperties(t *testing.T) {
	names, err := templateParameters(rawJSON(templateSchema))

	require.NoError(t, err)
	assert.Equal(t, []string{"policy"}, names)
}

func TestTemplateParametersOfNoSchemaIsEmpty(t *testing.T) {
	names, err := templateParameters(nil)

	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestRenderParametersFillsServiceAccount(t *testing.T) {
	got, err := renderParameters(
		rawJSON(templateSchema),
		rawJSON(`{"policy":"trust {{ .serviceAccount }}","size":3}`),
		TemplateData{ServiceAccount: "orders-camunda"},
	)

	require.NoError(t, err)
	assert.JSONEq(t, `{"policy":"trust orders-camunda","size":3}`, string(got.Raw))
}

func TestRenderParametersRendersTheStringsOfAnObjectValue(t *testing.T) {
	schema := `{"type":"object","properties":{"policy":{"type":"object","x-fleet-template":true}}}`

	got, err := renderParameters(
		rawJSON(schema),
		rawJSON(`{"policy":{"Principal":"{{ .namespace }}","Max":2}}`),
		TemplateData{Namespace: "team-a"},
	)

	require.NoError(t, err)
	assert.JSONEq(t, `{"policy":{"Principal":"team-a","Max":2}}`, string(got.Raw))
}

func TestRenderParametersNamesAnUnknownPlaceholder(t *testing.T) {
	_, err := renderParameters(rawJSON(templateSchema), rawJSON(`{"policy":"{{ .unknown }}"}`), TemplateData{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy")
	assert.Contains(t, err.Error(), "unknown")
}

func TestRenderParametersLeavesUnmarkedTextAlone(t *testing.T) {
	got, err := renderParameters(rawJSON(templateSchema), rawJSON(`{"size":"{{ .unknown }}"}`), TemplateData{})

	require.NoError(t, err)
	assert.JSONEq(t, `{"size":"{{ .unknown }}"}`, string(got.Raw))
}

func TestDecodeIntoLeavesTheZeroValueForNoParameters(t *testing.T) {
	type params struct{ Size int }
	got := params{}

	require.NoError(t, decodeInto(nil, &got))
	assert.Equal(t, params{}, got)
}

func TestDecodeIntoFillsTheType(t *testing.T) {
	type params struct {
		Size int `json:"size"`
	}
	var got params

	require.NoError(t, decodeInto([]byte(`{"size":3}`), &got))
	assert.Equal(t, params{Size: 3}, got)
}
