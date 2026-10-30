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
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

func TestTrustIRSANamesOnlyTheConnectorsServiceAccount(t *testing.T) {
	provider, err := awskit.ParseOIDCProvider(
		"arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC",
	)
	require.NoError(t, err)
	target := pluginsv1alpha1.TargetRef{
		Kind:                      "CamundaCluster",
		Name:                      "orders",
		ServiceAccountName:        "orders-connectors",
		AdditionalServiceAccounts: []string{"orders-camunda"},
	}

	doc, err := awskit.TrustPolicy(awskit.ModeIRSA, provider, "team-a", trustedServiceAccounts(target))

	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC"},
		"Action":"sts:AssumeRoleWithWebIdentity",
		"Condition":{"StringEquals":{"oidc.eks.eu-west-1.amazonaws.com/id/ABC:aud":"sts.amazonaws.com",
		"oidc.eks.eu-west-1.amazonaws.com/id/ABC:sub":["system:serviceaccount:team-a:orders-connectors"]}}}]}`,
		string(doc))
}

func TestTrustPodIdentityTrustsTheEKSPodsService(t *testing.T) {
	doc, err := awskit.TrustPolicy(
		awskit.ModePodIdentity,
		awskit.OIDCProvider{},
		"team-a",
		trustedServiceAccounts(pluginsv1alpha1.TargetRef{ServiceAccountName: "orders-connectors"}),
	)

	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Service":"pods.eks.amazonaws.com"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`,
		string(doc))
}

func TestTrustedServiceAccountsIgnoresTheAdditionalOnes(t *testing.T) {
	assert.Equal(t, []string{"a-connectors"}, trustedServiceAccounts(pluginsv1alpha1.TargetRef{
		ServiceAccountName:        "a-connectors",
		AdditionalServiceAccounts: []string{"a-camunda"},
	}))
	assert.Empty(t, trustedServiceAccounts(pluginsv1alpha1.TargetRef{AdditionalServiceAccounts: []string{"a-camunda"}}))
}

func TestWantedPoliciesIsTheSortedUnion(t *testing.T) {
	assert.Equal(t, []string{"arn:a", "arn:b", "arn:c"}, wantedPolicies(Params{
		ManagedPolicyARNs: []string{"arn:b", "arn:a"},
		ExtraPolicyARNs:   []string{"arn:c", "arn:a"},
	}))
	assert.Empty(t, wantedPolicies(Params{}))
}

func TestInlinePolicySizeCheck(t *testing.T) {
	pad := func(n int) json.RawMessage { return json.RawMessage(`{"Sid":"` + strings.Repeat("a", n) + `"}`) }
	overhead := len(`{"Sid":""}`)

	doc, size, err := inlinePolicyDocument(pad(awskit.MaxInlinePolicySize - overhead))
	require.NoError(t, err)
	assert.Equal(t, awskit.MaxInlinePolicySize, size)
	assert.Len(t, doc, awskit.MaxInlinePolicySize)

	_, _, err = inlinePolicyDocument(pad(awskit.MaxInlinePolicySize - overhead + 1))
	require.ErrorIs(t, err, errInlinePolicyTooLarge)
	assert.ErrorContains(t, err, "10241")

	doc, _, err = inlinePolicyDocument(json.RawMessage(
		"{\n  \"Sid\": \"" + strings.Repeat("a", awskit.MaxInlinePolicySize-overhead) + "\"\n}",
	))
	require.NoError(t, err, "white space does not count")
	assert.NotContains(t, string(doc), "\n", "the document is compact")

	doc, size, err = inlinePolicyDocument(nil)
	require.NoError(t, err)
	assert.Nil(t, doc)
	assert.Zero(t, size)

	doc, _, err = inlinePolicyDocument(json.RawMessage("null"))
	require.NoError(t, err)
	assert.Nil(t, doc, "null is no policy")
}

func TestSummaryWarnsAbove80Percent(t *testing.T) {
	role := awskit.Role{Name: "camunda-connectors-x"}

	items := summary(role, 2, 8192, "arn:b")

	assert.Equal(t, []string{"Role", "Attached policies", "Inline policy", "Permissions boundary"}, labels(items))
	assert.Equal(t, "camunda-connectors-x", items[0].Value)
	assert.Equal(t, "2", items[1].Value)
	assert.Equal(t, "8192 of 10240 characters", items[2].Value)
	assert.Equal(t, pluginsv1alpha1.SeverityInfo, items[2].Severity)
	assert.Equal(t, "arn:b", items[3].Value)
	assert.Equal(t, pluginsv1alpha1.SeverityWarning, summary(role, 2, 8193, "arn:b")[2].Severity)
}

func labels(items []pluginsv1alpha1.SummaryItem) []string {
	var out []string
	for _, i := range items {
		out = append(out, i.Label)
	}
	return out
}
