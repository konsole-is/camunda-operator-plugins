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

package aws_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/aws"
	"github.com/konsole-is/camunda-operator-plugins/internal/awskms"
)

// unreachable is a config with no API server behind it. Both refusals come
// before the manager talks to a server.
var unreachable = &rest.Config{Host: "https://127.0.0.1:1"}

func TestNewManagerRefusesNoController(t *testing.T) {
	_, err := aws.NewManager(unreachable, aws.Options{HealthProbeAddress: "0", MetricsAddress: "0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no controller is on")
}

func TestNewManagerNamesTheControllerThatRefused(t *testing.T) {
	_, err := aws.NewManager(unreachable, aws.Options{
		HealthProbeAddress: "0",
		MetricsAddress:     "0",
		KMS: &awskms.Options{
			Registrations: oneRegistration("aws-kms-key"),
			Settings:      validKMSSettings(),
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting up aws-kms")
	assert.Contains(t, err.Error(), "the KMS client is missing")
}

func oneRegistration(name string) []pluginsv1alpha1.PluginRegistration {
	return []pluginsv1alpha1.PluginRegistration{{ObjectMeta: metav1.ObjectMeta{Name: name}}}
}

func validKMSSettings() awskms.Settings {
	return awskms.Settings{Region: "eu-west-1", Partition: "aws", Account: "111122223333", AliasPrefix: "camunda"}
}
