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

package awsconnectorsrole

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func TestOldRoleRowsKeepsTheIAMRoleRowsOfOtherNames(t *testing.T) {
	rows := []pluginsv1alpha1.ResourceRef{
		{Kind: kindIAMRole, Name: "team-b-0123"},
		{Kind: kindIAMRole, Name: "camunda-0123"},
		{Kind: kindIAMRole, Name: "CAMUNDA-0123"},
		{Kind: kindPodIdentityAssociation, Name: "orders-camunda"},
		{Kind: "Grant", Name: "orders-backup"},
	}

	assert.Equal(
		t,
		[]pluginsv1alpha1.ResourceRef{{Kind: kindIAMRole, Name: "camunda-0123"}},
		oldRoleRows(rows, "team-b-0123"),
	)
	assert.Len(t, oldRoleRows(rows, ""), 2, "no current name keeps each role once")
}

// A change of the role of the name in AWS sends no watch event, so the
// request must check it again by itself.
func TestReconcileRetriesARoleNameInUse(t *testing.T) {
	fake := awsfake.NewIAM()
	opts := validOptions()
	opts.Clients.IAM = fake
	p, err := newPlugin(opts)
	require.NoError(t, err)
	request := &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "orders-identity", UID: "uid-1"},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Registration: RegistrationRole,
			ClaimRef:     pluginsv1alpha1.ClaimRef{Name: "orders"},
			Target: pluginsv1alpha1.TargetRef{
				Kind:               "CamundaCluster",
				Name:               "orders",
				ServiceAccountName: "orders-connectors",
			},
		},
	}
	_, _, err = awskit.EnsureRole(context.Background(), fake, awskit.RoleSpec{
		Name:        p.roleName(request),
		TrustPolicy: []byte(`{}`),
		Tags:        awskit.ExtraTags(nil).OwnerTags("team-a", "orders", "orders-identity", "uid-2"),
	})
	require.NoError(t, err)

	res, err := p.Reconcile(context.Background(), pluginkit.Request[Params, map[string]string]{Object: request})

	require.NoError(t, err)
	require.NotNil(t, res.Pending)
	assert.Equal(t, ReasonRoleNameInUse, res.Pending.Reason)
	assert.Equal(t, roleInUseRequeue, res.RequeueAfter)
}
