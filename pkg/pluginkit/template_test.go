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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestParseTemplate(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		kind    TemplateKind
		wantErr string // empty: no error
	}{
		{"namespace passes", "{{ .namespace }}", TemplateGrant, ""},
		{"claim passes", "{{ .claim }}", TemplateGrant, ""},
		{"request passes", "{{ .request }}", TemplateGrant, ""},
		{"serviceAccount passes", "{{ .serviceAccount }}", TemplateParameter, ""},
		{"a parameter passes", "{{ .parameters.x }}", TemplateGrant, ""},
		{"an output passes", "{{ .outputs.y }}", TemplateGrant, ""},
		{"pods passes in a metric query", `up{pod=~"{{ .pods }}"}`, TemplateMetricQuery, ""},
		{"a parameter passes in a metric query", "{{ .parameters.x }}", TemplateMetricQuery, ""},
		{"an output passes in a metric query", "{{ .outputs.y }}", TemplateMetricQuery, ""},
		{"pods fails in a grant", "{{ .pods }}", TemplateGrant, "pods"},
		{"pods fails in a parameter", "{{ .pods }}", TemplateParameter, "pods"},
		{"an output fails in a parameter", "{{ .outputs.y }}", TemplateParameter, "cannot read the outputs"},
		{"an unknown name fails", "{{ .unknown }}", TemplateGrant, "unknown"},
		{"parameters without a name fails", "{{ .parameters }}", TemplateGrant, "parameters"},
		{"a nested parameter fails", "{{ .parameters.x.y }}", TemplateGrant, "parameters.x.y"},
		{"if fails", "{{ if .x }}a{{ end }}", TemplateGrant, "if"},
		{"range fails", "{{ range .x }}a{{ end }}", TemplateGrant, "range"},
		{"a pipeline fails", `{{ .x | printf "%s" }}`, TemplateGrant, "pipeline"},
		{"a function call fails", `{{ printf "%s" .namespace }}`, TemplateGrant, "printf"},
		{"a variable fails", "{{ $x := .namespace }}", TemplateGrant, "variable"},
		{"invalid syntax fails", "{{ .namespace", TemplateGrant, "unclosed action"},
		{"plain text passes", "arn:aws:s3:::bucket", TemplateGrant, ""},
		{"a comment fails", "{{/* note */}}", TemplateGrant, "comment"},
		{"a define fails", `{{ define "x" }}{{ if .unknown }}a{{ end }}{{ end }}`, TemplateGrant, "define"},
		{"a block fails", `{{ block "x" . }}a{{ end }}`, TemplateGrant, "define"},
		{"a define of the root fails", `{{ define "" }}{{ printf "%s" .namespace }}{{ end }}`, TemplateGrant, "define"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTemplate(tc.text, tc.kind)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestRenderFillsEveryPlaceholder(t *testing.T) {
	tmpl, err := ParseTemplate(
		"{{ .namespace }}/{{ .claim }}/{{ .request }}/{{ .serviceAccount }}/{{ .parameters.size }}/{{ .outputs.bucket }}",
		TemplateGrant,
	)
	require.NoError(t, err)

	got, err := tmpl.Render(TemplateData{
		Namespace:      "team-a",
		Claim:          "orders",
		Request:        "orders-backup-storage",
		ServiceAccount: "orders-camunda",
		Parameters:     map[string]any{"size": 3},
		Outputs:        map[string]string{"bucket": "b-1"},
	})

	require.NoError(t, err)
	assert.Equal(t, "team-a/orders/orders-backup-storage/orders-camunda/3/b-1", got)
}

func TestRenderFillsPodsInAMetricQuery(t *testing.T) {
	tmpl, err := ParseTemplate(`up{pod=~"{{ .pods }}"}`, TemplateMetricQuery)
	require.NoError(t, err)

	got, err := tmpl.Render(TemplateData{Pods: "a|b"})

	require.NoError(t, err)
	assert.Equal(t, `up{pod=~"a|b"}`, got)
}

func TestRenderNamesAMissingParameter(t *testing.T) {
	tmpl, err := ParseTemplate("{{ .parameters.size }}", TemplateGrant)
	require.NoError(t, err)

	_, err = tmpl.Render(TemplateData{})

	assert.EqualError(t, err, `parameter "size" has no value`)
}

func TestRenderNamesAMissingOutput(t *testing.T) {
	tmpl, err := ParseTemplate("{{ .outputs.bucket }}", TemplateGrant)
	require.NoError(t, err)

	_, err = tmpl.Render(TemplateData{Outputs: map[string]string{"other": "x"}})

	assert.EqualError(t, err, `output "bucket" has no value`)
}

// The conformance module and the template preview of the fleet manager, in
// the operator repository, hold the same cases in the same order.
func TestRenderPrintsOnlyAStringANumberOrABoolean(t *testing.T) {
	cases := []struct {
		name       string
		parameters string // spec.parameters as the API server serves it
		want       string
		wantErr    string // empty: no error
	}{
		{
			"a list",
			`{"v":["a","b"]}`,
			"",
			`parameter "v" is a list: a template prints only a string, a number, or a boolean`,
		},
		{
			"an object",
			`{"v":{"a":1,"k":"v"}}`,
			"",
			`parameter "v" is an object: a template prints only a string, a number, or a boolean`,
		},
		{"null", `{"v":null}`, "", `parameter "v" is null: a template prints only a string, a number, or a boolean`},
		// The API server stores the number 1.0 as 1, and 1e3 as 1000.
		{"the number 1.0", `{"v":1}`, "1", ""},
		{"the number 1e3", `{"v":1000}`, "1000", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := ParseTemplate("{{ .parameters.v }}", TemplateGrant)
			require.NoError(t, err)
			var data TemplateData
			require.NoError(t, setTemplateParameters(&data, &apiextensionsv1.JSON{Raw: []byte(tc.parameters)}))

			got, err := tmpl.Render(data)

			if tc.wantErr != "" {
				assert.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRenderJSONKeepsAQuoteInsideItsString(t *testing.T) {
	got, err := RenderJSON(
		[]byte(`{"Resource":"arn:aws:s3:::{{ .outputs.bucket }}"}`),
		TemplateGrant,
		TemplateData{Outputs: map[string]string{"bucket": `a"b`}},
	)

	require.NoError(t, err)
	require.True(t, json.Valid(got), "rendered %s", got)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got, &decoded))
	assert.Equal(t, map[string]any{"Resource": `arn:aws:s3:::a"b`}, decoded)
}

func TestRenderJSONRendersNestedStringsAndKeepsOtherValues(t *testing.T) {
	got, err := RenderJSON(
		[]byte(`{"Action":["s3:GetObject","{{ .parameters.extra }}"],`+
			`"Condition":{"StringEquals":{"aws:ns":"{{ .namespace }}"}},"Max":5,"Enabled":true,"None":null,"Empty":""}`),
		TemplateGrant,
		TemplateData{Namespace: "team-a", Parameters: map[string]any{"extra": "s3:PutObject"}},
	)

	require.NoError(t, err)
	assert.JSONEq(t, `{"Action":["s3:GetObject","s3:PutObject"],`+
		`"Condition":{"StringEquals":{"aws:ns":"team-a"}},"Max":5,"Enabled":true,"None":null,"Empty":""}`, string(got))
}

func TestRenderJSONNamesAnUnknownPlaceholder(t *testing.T) {
	_, err := RenderJSON([]byte(`{"Resource":"{{ .unknown }}"}`), TemplateGrant, TemplateData{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")
}

func TestRenderJSONRefusesInvalidJSON(t *testing.T) {
	_, err := RenderJSON([]byte(`{"Resource":`), TemplateGrant, TemplateData{})

	assert.Error(t, err)
}
