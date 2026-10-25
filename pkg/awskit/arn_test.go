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

func TestParseOIDCProviderReadsTheIssuer(t *testing.T) {
	p, err := ParseOIDCProvider("arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC")

	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC", p.ARN)
	assert.Equal(t, "oidc.eks.eu-west-1.amazonaws.com/id/ABC", p.Issuer)
}

func TestParseOIDCProviderRefusesAnotherARN(t *testing.T) {
	for _, arn := range []string{
		"arn:aws:iam::111122223333:role/x",
		"arn:aws:iam::111122223333:oidc-provider/",
		"not an arn",
		"",
	} {
		_, err := ParseOIDCProvider(arn)

		assert.ErrorContains(t, err, "is not the ARN of an IAM OIDC provider", arn)
	}
}

func TestRoleConsoleURLUsesTheConsoleOfThePartition(t *testing.T) {
	for _, tc := range []struct{ arn, want string }{
		{
			"arn:aws:iam::111122223333:role/camunda-x",
			"https://console.aws.amazon.com/iam/home#/roles/details/camunda-x",
		},
		{
			"arn:aws-cn:iam::111122223333:role/camunda-x",
			"https://console.amazonaws.cn/iam/home#/roles/details/camunda-x",
		},
		{
			"arn:aws-us-gov:iam::111122223333:role/camunda-x",
			"https://console.amazonaws-us-gov.com/iam/home#/roles/details/camunda-x",
		},
	} {
		got, err := RoleConsoleURL(tc.arn)

		require.NoError(t, err, tc.arn)
		assert.Equal(t, tc.want, got)
	}
}

func TestRoleConsoleURLRefusesAnUnknownPartitionAndAnotherARN(t *testing.T) {
	_, err := RoleConsoleURL("arn:aws-moon:iam::111122223333:role/camunda-x")
	assert.ErrorContains(t, err, `partition "aws-moon"`)

	_, err = RoleConsoleURL("arn:aws:iam::111122223333:policy/x")
	assert.ErrorContains(t, err, "is not the ARN of an IAM role")
}
