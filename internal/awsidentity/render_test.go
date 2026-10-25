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

package awsidentity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func raw(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

func renderRequest() *pluginsv1alpha1.PluginRequest {
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "orders-backup"},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			ClaimRef: pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: "orders"},
			Target: pluginsv1alpha1.TargetRef{
				Kind:               "CamundaCluster",
				Name:               "orders",
				ServiceAccountName: "orders-camunda",
			},
		},
	}
}

func renderRegistration(templates ...pluginsv1alpha1.GrantTemplate) *pluginsv1alpha1.PluginRegistration {
	return &pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "s3-test"},
		Spec:       pluginsv1alpha1.PluginRegistrationSpec{GrantTemplates: templates},
	}
}

func TestRenderGrantFillsTheTemplateFromTheRequest(t *testing.T) {
	reg := renderRegistration(pluginsv1alpha1.GrantTemplate{
		Name:   "bucket-read-write",
		Format: pluginsv1alpha1.GrantFormatAWSIAMStatement,
		Template: raw(`{"Action":["s3:GetObject"],` +
			`"Resource":"arn:aws:s3:::{{ .outputs.bucket }}/{{ .parameters.prefix }}{{ .claim }}-{{ .namespace }}"}`),
	})
	req := renderRequest()
	req.Spec.Parameters = raw(`{"prefix":"zeebe/"}`)
	req.Status.Outputs = map[string]string{"bucket": "b-1"}

	got, err := renderGrant(req, reg, "bucket-read-write")

	require.NoError(t, err)
	assert.JSONEq(t, `{"Action":["s3:GetObject"],"Resource":"arn:aws:s3:::b-1/zeebe/orders-team-a"}`, string(got))
}

func TestRenderGrantRendersATemplateParameterFirst(t *testing.T) {
	reg := renderRegistration(
		pluginsv1alpha1.GrantTemplate{Name: "scoped", Template: raw(`{"Resource":"{{ .parameters.path }}"}`)},
	)
	reg.Spec.Parameters = raw(`{"type":"object","properties":{"path":{"type":"string","x-fleet-template":true}}}`)
	req := renderRequest()
	req.Spec.Parameters = raw(`{"path":"arn:aws:s3:::{{ .namespace }}-x"}`)

	got, err := renderGrant(req, reg, "scoped")

	require.NoError(t, err)
	assert.JSONEq(t, `{"Resource":"arn:aws:s3:::team-a-x"}`, string(got))
}

func TestRenderGrantRefusesAnUnknownTemplate(t *testing.T) {
	_, err := renderGrant(renderRequest(), renderRegistration(), "gone")

	assert.ErrorContains(t, err, `"gone"`)
	assert.ErrorContains(t, err, "s3-test")
}

func TestRenderGrantNamesAMissingOutput(t *testing.T) {
	reg := renderRegistration(
		pluginsv1alpha1.GrantTemplate{Name: "t", Template: raw(`{"Resource":"{{ .outputs.bucket }}"}`)},
	)

	_, err := renderGrant(renderRequest(), reg, "t")

	assert.ErrorContains(t, err, `output "bucket" has no value`)
}
