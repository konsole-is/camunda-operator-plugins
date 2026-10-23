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
	assert.NoError(t, ValidateParameters(rawJSON(sizeSchema), rawJSON(`{"size":3}`)))
}

func TestValidateParametersNamesTheField(t *testing.T) {
	err := ValidateParameters(rawJSON(sizeSchema), rawJSON(`{"size":0}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.parameters.size")

	err = ValidateParameters(rawJSON(sizeSchema), rawJSON(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.parameters.size")
	assert.Contains(t, err.Error(), "Required")
}

func TestValidateParametersAcceptsAnythingWithoutASchema(t *testing.T) {
	assert.NoError(t, ValidateParameters(nil, rawJSON(`{"x":1}`)))
}

func TestValidateParametersChecksAbsentParametersAsAnEmptyObject(t *testing.T) {
	err := ValidateParameters(rawJSON(sizeSchema), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.parameters.size")
}

func TestValidateParametersRefusesASchemaThatIsNotJSON(t *testing.T) {
	assert.Error(t, ValidateParameters(rawJSON(`{"type":`), rawJSON(`{}`)))
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

func TestRenderParametersRefusesAnOutput(t *testing.T) {
	_, err := renderParameters(
		rawJSON(templateSchema),
		rawJSON(`{"policy":"arn:aws:s3:::{{ .outputs.bucket }}"}`),
		TemplateData{Outputs: map[string]string{"bucket": "b-1"}},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `parameter "policy"`)
	assert.Contains(t, err.Error(), "cannot read the outputs")
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
