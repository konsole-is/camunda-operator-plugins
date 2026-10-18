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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func grantRequest(inputs map[string]string) *pluginsv1alpha1.PluginRequest {
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-backup-storage", Namespace: "team-a"},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Registration: "s3-bucket",
			Target:       pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: "orders"},
			Inputs:       inputs,
		},
	}
}

func grantRegistration(templates ...pluginsv1alpha1.GrantTemplate) *pluginsv1alpha1.PluginRegistration {
	return &pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "s3-bucket"},
		Spec:       pluginsv1alpha1.PluginRegistrationSpec{GrantTemplates: templates},
	}
}

func grantTemplate(name, template string, requires ...string) pluginsv1alpha1.GrantTemplate {
	return pluginsv1alpha1.GrantTemplate{
		Name:           name,
		Format:         pluginsv1alpha1.GrantFormatAWSIAMStatement,
		Template:       rawJSON(template),
		RequiresInputs: requires,
	}
}

func grantNames(grants []*pluginsv1alpha1.Grant) []string {
	names := make([]string, 0, len(grants))
	for _, g := range grants {
		names = append(names, g.Name)
	}
	return names
}

func TestRenderGrantsWritesOneGrantForEachTemplate(t *testing.T) {
	reg := grantRegistration(
		grantTemplate("bucket-read-write", `{"Action":"s3:*","Resource":"arn:aws:s3:::{{ .outputs.bucket }}"}`),
		grantTemplate("bucket-list", `{"Action":"s3:ListBucket"}`),
	)

	grants, err := renderGrants(grantRequest(nil), reg, TemplateData{Outputs: map[string]string{"bucket": "b-1"}})

	require.NoError(t, err)
	require.Len(t, grants, 2)
	g := grants[0]
	assert.Equal(t, "orders-backup-storage-bucket-read-write", g.Name)
	assert.Equal(t, "team-a", g.Namespace)
	assert.Equal(t, map[string]string{
		pluginsv1alpha1.LabelRegistration: "s3-bucket",
		pluginsv1alpha1.LabelRequest:      "orders-backup-storage",
		pluginsv1alpha1.LabelCluster:      "orders",
	}, g.Labels)
	assert.Equal(t, "orders", g.Spec.Cluster.Name)
	assert.Equal(t, "bucket-read-write", g.Spec.Template)
	assert.Equal(t, pluginsv1alpha1.GrantFormatAWSIAMStatement, g.Spec.Format)
	assert.JSONEq(t, `{"Action":"s3:*","Resource":"arn:aws:s3:::b-1"}`, string(g.Spec.Fragment.Raw))
	assert.Empty(t, g.OwnerReferences)
}

func TestRenderGrantsSkipsATemplateWhoseInputHasNoValue(t *testing.T) {
	reg := grantRegistration(
		grantTemplate("bucket-read-write", `{"Action":"s3:*"}`),
		grantTemplate("bucket-key-use", `{"Action":"kms:Decrypt","Resource":"{{ .parameters.key }}"}`, "key"),
	)

	for name, inputs := range map[string]map[string]string{
		"no input":    nil,
		"empty input": {"key": ""},
	} {
		t.Run(name, func(t *testing.T) {
			grants, err := renderGrants(grantRequest(inputs), reg, TemplateData{})

			require.NoError(t, err)
			assert.Equal(t, []string{"orders-backup-storage-bucket-read-write"}, grantNames(grants))
		})
	}
}

func TestRenderGrantsRendersATemplateWhoseInputsHaveValues(t *testing.T) {
	reg := grantRegistration(
		grantTemplate("bucket-read-write", `{"Action":"s3:*"}`),
		grantTemplate("bucket-key-use", `{"Action":"kms:Decrypt"}`, "key"),
	)

	grants, err := renderGrants(grantRequest(map[string]string{"key": "arn:aws:kms:eu-west-1:1:key/k"}), reg, TemplateData{})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"orders-backup-storage-bucket-read-write",
		"orders-backup-storage-bucket-key-use",
	}, grantNames(grants))
}

func TestRenderGrantsNamesAnUnknownPlaceholder(t *testing.T) {
	reg := grantRegistration(grantTemplate("bucket-read-write", `{"Resource":"{{ .unknown }}"}`))

	_, err := renderGrants(grantRequest(nil), reg, TemplateData{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket-read-write")
	assert.Contains(t, err.Error(), "unknown")
}

func TestRenderGrantsRefusesATargetThatIsNotACluster(t *testing.T) {
	req := grantRequest(nil)
	req.Spec.Target.Kind = "CamundaManagementCluster"

	_, err := renderGrants(req, grantRegistration(grantTemplate("read", `{}`)), TemplateData{})

	assert.ErrorContains(t, err, "grants need a CamundaCluster target")
}

func TestRenderGrantsAcceptsAnyTargetWithoutTemplates(t *testing.T) {
	req := grantRequest(nil)
	req.Spec.Target.Kind = "CamundaManagementCluster"

	grants, err := renderGrants(req, grantRegistration(), TemplateData{})

	require.NoError(t, err)
	assert.Empty(t, grants)
}

func TestGrantNameStaysUniqueAndShort(t *testing.T) {
	request := strings.Repeat("r", 63)
	template := strings.Repeat("t", 20)
	assert.LessOrEqual(t, len(GrantName(request, template)), 63)

	shared := strings.Repeat("s", 40)
	a, b := GrantName(request, shared+"-a"), GrantName(request, shared+"-b")
	assert.NotEqual(t, a, b)
	assert.LessOrEqual(t, len(a), 63)

	assert.Equal(t, "orders-backup-storage-bucket-read-write", GrantName("orders-backup-storage", "bucket-read-write"))
}

func TestGrantNameDoesNotEndTheCutWithADash(t *testing.T) {
	request := strings.Repeat("r", 53) + "-x"

	name := GrantName(request, strings.Repeat("t", 20))

	assert.NotContains(t, name, "--")
}
