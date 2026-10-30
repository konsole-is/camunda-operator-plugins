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

package awskms

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validSettings() Settings {
	return Settings{Region: "eu-west-1", Partition: "aws", Account: "111122223333", AliasPrefix: "camunda"}
}

func TestSettingsValidate(t *testing.T) {
	require.NoError(t, validSettings().Validate())

	for _, tc := range []struct {
		name  string
		edit  func(*Settings)
		field string
	}{
		{"empty region", func(s *Settings) { s.Region = "" }, "region"},
		{"no account", func(s *Settings) { s.Account = "" }, "account"},
		{"reserved prefix", func(s *Settings) { s.AliasPrefix = "aws" }, "alias prefix"},
		{"bad administrator", func(s *Settings) { s.KeyAdministratorARNs = []string{"not-an-arn"} }, "key administrator"},
		{"bad driver", func(s *Settings) { s.CSIDriverRoleARN = "arn:aws:s3:::bucket" }, "CSI driver role"},
	} {
		s := validSettings()
		tc.edit(&s)

		assert.ErrorContains(t, s.Validate(), tc.field, tc.name)
	}
}

// callerSTS returns the caller ARN arn.
type callerSTS struct{ arn string }

func (c callerSTS) GetCallerIdentity(
	context.Context,
	*sts.GetCallerIdentityInput,
	...func(*sts.Options),
) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{Account: aws.String("111122223333"), Arn: aws.String(c.arn)}, nil
}

func TestResolveAccount(t *testing.T) {
	account, partition, err := ResolveAccount(context.Background(), callerSTS{"arn:aws-cn:iam::111122223333:user/x"})

	require.NoError(t, err)
	assert.Equal(t, "111122223333", account)
	assert.Equal(t, "aws-cn", partition)
}
