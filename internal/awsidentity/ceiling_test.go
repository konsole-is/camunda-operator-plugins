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

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestCeilingAllowsActionsInsideThePatterns(t *testing.T) {
	c, err := NewCeiling([]string{"s3:*", "kms:Decrypt", "kms:GenerateDataKey*"})
	require.NoError(t, err)

	assert.Nil(
		t,
		c.check([]byte(`{"Effect":"Allow","Action":["s3:GetObject","S3:PutObject","s3:Get*"],"Resource":"*"}`)),
	)
	assert.Nil(t, c.check([]byte(`{"Effect":"Allow","Action":"kms:GenerateDataKeyWithoutPlaintext","Resource":"*"}`)))
	assert.Nil(t, c.check([]byte(`{"Effect":"Deny","Action":"kms:decrypt","Resource":"*"}`)))
}

func TestCeilingRefusesAnActionOutsideThePatterns(t *testing.T) {
	c, err := NewCeiling([]string{"s3:GetObject", "kms:Get?"})
	require.NoError(t, err)

	for fragment, action := range map[string]string{
		`{"Effect":"Allow","Action":["s3:GetObject","s3:Get*"],"Resource":"*"}`: "s3:Get*",
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}`:             "iam:PassRole",
		`{"Effect":"Allow","Action":"kms:GetAB","Resource":"*"}`:                "kms:GetAB",
	} {
		got := c.check([]byte(fragment))

		require.NotNil(t, got, fragment)
		assert.Equal(t, pluginsv1alpha1.ReasonOverCeiling, got.reason)
		assert.Contains(t, got.message, action)
	}
}

func TestCeilingRejectsAStatementItCannotCheck(t *testing.T) {
	c, err := NewCeiling([]string{"s3:*"})
	require.NoError(t, err)

	for _, fragment := range []string{
		`[]`,
		`not json`,
		`{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}`,
		`{"Effect":"Allow","Resource":"*"}`,
		`{"Effect":"Allow","Action":[],"Resource":"*"}`,
		`{"Effect":"Allow","Action":[1],"Resource":"*"}`,
		`{"Effect":"Maybe","Action":"s3:GetObject"}`,
	} {
		got := c.check([]byte(fragment))

		require.NotNil(t, got, fragment)
		assert.Equal(t, pluginsv1alpha1.ReasonRejected, got.reason, fragment)
	}
}

func TestNewCeilingRefusesAnEmptyListAndABadPattern(t *testing.T) {
	_, err := NewCeiling(nil)
	assert.ErrorContains(t, err, "no allowed action")

	for _, p := range []string{"s3", "s3:Get Object", ":Get", "s3:", "s3:a:b"} {
		_, err := NewCeiling([]string{p})

		assert.ErrorContains(t, err, `"`+p+`"`, p)
	}
}
