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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

func TestOwnsNeedsTheRegistrationAndTheClusterOfTheGrant(t *testing.T) {
	request := &pluginsv1alpha1.PluginRequest{Spec: pluginsv1alpha1.PluginRequestSpec{
		Registration: "s3",
		Target:       pluginsv1alpha1.TargetRef{Kind: kindCamundaCluster, Name: "orders"},
	}}
	grant := func(registration, label, cluster string) *pluginsv1alpha1.Grant {
		return &pluginsv1alpha1.Grant{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
				pluginsv1alpha1.LabelRegistration: registration,
				pluginsv1alpha1.LabelCluster:      label,
			}},
			Spec: pluginsv1alpha1.GrantSpec{Cluster: v1.ClusterRef{Name: cluster}},
		}
	}

	assert.True(t, owns(request, grant("s3", "orders", "orders")))
	assert.False(t, owns(request, grant("kms", "orders", "orders")), "another registration")
	assert.False(t, owns(request, grant("s3", "billing", "orders")), "another cluster label")
	assert.False(t, owns(request, grant("s3", "orders", "billing")), "another spec.cluster")

	plane := request.DeepCopy()
	plane.Spec.Target.Kind = "CamundaManagementCluster"
	assert.False(t, owns(plane, grant("s3", "orders", "orders")), "another target kind")
}
