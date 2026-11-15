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

package awskit_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var stsCases = []awsCase{
	{"gives the account and the ARN of the caller", func(t *testing.T, a account) {
		out, err := a.sts.GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})

		require.NoError(t, err)
		assert.Equal(t, a.id, aws.ToString(out.Account))
		parsed, err := arn.Parse(aws.ToString(out.Arn))
		require.NoError(t, err, "the ARN of the caller: %q", aws.ToString(out.Arn))
		assert.Equal(t, "aws", parsed.Partition)
		assert.Equal(t, a.id, parsed.AccountID)
	}},
}
