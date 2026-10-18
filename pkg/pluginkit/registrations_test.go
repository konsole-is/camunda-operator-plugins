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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestLoadRegistrationsReadsTheChartShape(t *testing.T) {
	registrations, err := LoadRegistrations(filepath.Join("testdata", "registrations.yaml"))

	require.NoError(t, err)
	require.Len(t, registrations, 2)

	full := registrations[0]
	assert.Equal(t, "s3-bucket", full.Name)
	assert.Equal(t, []string{"backup-storage"}, full.Spec.Sections)
	assert.Equal(t, "New S3 bucket", full.Spec.Label)
	assert.Equal(t, pluginsv1alpha1.ModeDefault, full.Spec.Mode)
	assert.JSONEq(t, `{"type":"object","properties":{"size":{"type":"integer","minimum":1}}}`,
		string(full.Spec.Parameters.Raw))
	require.Len(t, full.Spec.GrantTemplates, 1)
	assert.Equal(t, "bucket-read-write", full.Spec.GrantTemplates[0].Name)
	assert.Equal(t, []string{"key"}, full.Spec.GrantTemplates[0].RequiresInputs)
	assert.JSONEq(t, `{"Action":"s3:*","Resource":"arn:aws:s3:::{{ .outputs.bucket }}"}`,
		string(full.Spec.GrantTemplates[0].Template.Raw))
	assert.Equal(t, []pluginsv1alpha1.DeletionPolicy{
		pluginsv1alpha1.DeletionPolicyDelete,
		pluginsv1alpha1.DeletionPolicyOrphan,
	}, full.Spec.DeletionPolicies)

	assert.Equal(t, "s3-bucket-internal", registrations[1].Name)
}

func TestLoadRegistrationsRefusesAnUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registrations.yaml")
	require.NoError(t, os.WriteFile(path, []byte("- name: x\n  lable: X\n"), 0o600))

	_, err := LoadRegistrations(path)

	assert.ErrorContains(t, err, "lable")
}

func TestLoadRegistrationsRefusesAnItemWithoutAName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registrations.yaml")
	require.NoError(t, os.WriteFile(path, []byte("- label: X\n"), 0o600))

	_, err := LoadRegistrations(path)

	assert.ErrorContains(t, err, "has no name")
}

func TestLoadRegistrationsNamesAMissingFile(t *testing.T) {
	_, err := LoadRegistrations(filepath.Join(t.TempDir(), "missing.yaml"))

	assert.ErrorContains(t, err, "missing.yaml")
}

func TestServiceAccountFromEnvReadsBothVariables(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "camunda-operator-plugins")
	t.Setenv("SERVICE_ACCOUNT_NAME", "skeleton")

	ref, err := ServiceAccountFromEnv()

	require.NoError(t, err)
	assert.Equal(t, pluginsv1alpha1.ServiceAccountRef{Namespace: "camunda-operator-plugins", Name: "skeleton"}, ref)
}

func TestServiceAccountFromEnvNamesTheMissingVariable(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "camunda-operator-plugins")
	t.Setenv("SERVICE_ACCOUNT_NAME", "")

	_, err := ServiceAccountFromEnv()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SERVICE_ACCOUNT_NAME")
	assert.NotContains(t, err.Error(), "POD_NAMESPACE")
}
