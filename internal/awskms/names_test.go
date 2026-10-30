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
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

func TestAliasName(t *testing.T) {
	long := strings.Repeat("c", 300)

	for _, tc := range []struct{ prefix, namespace, claim, want string }{
		{"camunda", "team-a", "orders", "alias/camunda/team-a/orders"},
		{"cpo-ci-manual-1", "team-a", "orders", "alias/cpo-ci-manual-1/team-a/orders"},
		{
			"camunda", strings.Repeat("n", 63), strings.Repeat("c", 40),
			"alias/camunda/" + strings.Repeat("n", 63) + "/" + strings.Repeat("c", 40),
		},
		{"camunda", "team-a", "a.b/c", "alias/camunda/team-a/a_b_c"},
	} {
		assert.Equal(t, tc.want, aliasName(tc.prefix, tc.namespace, tc.claim))
	}

	a := aliasName("camunda", "team-a", long)
	b := aliasName("camunda", "team-a", long[:250]+"dddddddddd"+long[260:])
	assert.Len(t, a, 256)
	assert.True(t, strings.HasPrefix(a, "alias/camunda/team-a/"))
	assert.NotEqual(t, a, b, "two long claims with the same start get two aliases")
}

func TestKeyTagsMergeTheExtraTags(t *testing.T) {
	s := Settings{ExtraTags: awskit.ExtraTags{"cpo-ci-run": "cpo-ci-manual-1"}}

	tags := keyTags(s, "team-a", "orders", "orders-key")

	assert.Equal(t, []types.Tag{
		{TagKey: aws.String(awskit.TagClaim), TagValue: aws.String("orders")},
		{TagKey: aws.String(awskit.TagNamespace), TagValue: aws.String("team-a")},
		{TagKey: aws.String(awskit.TagRequest), TagValue: aws.String("orders-key")},
		{TagKey: aws.String("cpo-ci-run"), TagValue: aws.String("cpo-ci-manual-1")},
	}, tags)
}
