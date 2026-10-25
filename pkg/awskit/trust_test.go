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

package awskit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var provider = OIDCProvider{
	ARN:    "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC",
	Issuer: "oidc.eks.eu-west-1.amazonaws.com/id/ABC",
}

func TestTrustPolicyWithIRSATrustsEachServiceAccount(t *testing.T) {
	doc, err := TrustPolicy(ModeIRSA, provider, "team-a", []string{"orders-es-es", "orders-camunda", "orders-camunda"})

	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{
		"Effect":"Allow",
		"Principal":{"Federated":"arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC"},
		"Action":"sts:AssumeRoleWithWebIdentity",
		"Condition":{"StringEquals":{
			"oidc.eks.eu-west-1.amazonaws.com/id/ABC:aud":"sts.amazonaws.com",
			"oidc.eks.eu-west-1.amazonaws.com/id/ABC:sub":[
				"system:serviceaccount:team-a:orders-camunda",
				"system:serviceaccount:team-a:orders-es-es"]}}}]}`, string(doc))
}

func TestTrustPolicyWithPodIdentityTrustsTheEKSService(t *testing.T) {
	doc, err := TrustPolicy(ModePodIdentity, OIDCProvider{}, "team-a", []string{"orders-camunda"})

	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Service":"pods.eks.amazonaws.com"},
		"Action":["sts:AssumeRole","sts:TagSession"]}]}`, string(doc))
}

func TestTrustPolicyWithoutServiceAccountsDeniesThePrincipalOfTheMode(t *testing.T) {
	irsa, err := TrustPolicy(ModeIRSA, provider, "team-a", nil)
	require.NoError(t, err)
	podIdentity, err := TrustPolicy(ModePodIdentity, OIDCProvider{}, "team-a", nil)
	require.NoError(t, err)

	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny",
		"Principal":{"Federated":"arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC"},
		"Action":"sts:AssumeRoleWithWebIdentity"}]}`, string(irsa))
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny",
		"Principal":{"Service":"pods.eks.amazonaws.com"},
		"Action":["sts:AssumeRole","sts:TagSession"]}]}`, string(podIdentity))
}

func TestTrustPolicyWithIRSANeedsAProvider(t *testing.T) {
	_, err := TrustPolicy(ModeIRSA, OIDCProvider{}, "team-a", []string{"x"})

	assert.ErrorContains(t, err, "OIDC provider")
}

func TestTrustPolicyRefusesAnUnknownMode(t *testing.T) {
	_, err := TrustPolicy(Mode("other"), provider, "team-a", []string{"x"})

	assert.ErrorContains(t, err, `"other"`)
}

func TestParseModeReadsEachMode(t *testing.T) {
	for s, want := range map[string]Mode{"irsa": ModeIRSA, "podIdentity": ModePodIdentity} {
		got, err := ParseMode(s)

		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestParseModeRefusesAnUnknownMode(t *testing.T) {
	_, err := ParseMode("irsa2")

	assert.ErrorContains(t, err, `"irsa2"`)
}
