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

package awss3

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

func TestBucketNameFitsS3ForLongNamespacesAndClaims(t *testing.T) {
	name := bucketName("camunda", strings.Repeat("n", 63), strings.Repeat("c", 253))

	assert.LessOrEqual(t, len(name), 63)
	assert.Regexp(t, `^camunda-n+-[0-9a-f]{16}$`, name)
	assert.Regexp(t, `^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`, name)
}

func TestBucketNameNamesThePrefixTheNamespaceAndTheClaim(t *testing.T) {
	assert.Regexp(t, `^camunda-team-a-orders-[0-9a-f]{16}$`, bucketName("camunda", "team-a", "orders"))
}

func TestBucketNameKeepsTheLongestPrefixWhole(t *testing.T) {
	prefix := strings.Repeat("p", 40)

	assert.True(t, strings.HasPrefix(bucketName(prefix, strings.Repeat("n", 63), "c"), prefix+"-"))
}

func TestBucketNameKeepsTwoClaimsApart(t *testing.T) {
	assert.NotEqual(t, bucketName("camunda", "a-b", "c"), bucketName("camunda", "a", "b-c"))
	assert.NotEqual(t,
		bucketName("camunda", strings.Repeat("n", 63), strings.Repeat("c", 40)+"x"),
		bucketName("camunda", strings.Repeat("n", 63), strings.Repeat("c", 40)+"y"),
		"names that differ only after the cut")
}

func TestBucketNameKeepsTwoPrefixesApart(t *testing.T) {
	namespace := strings.Repeat("eu-", 19) + "eu"

	assert.NotEqual(t, bucketName("prod", namespace, "orders"), bucketName("prod-eu", namespace, "orders"),
		"prefixes whose difference the cut removes")
}

func TestBucketNameReplacesDots(t *testing.T) {
	assert.NotContains(t, bucketName("camunda", "team", "orders.v2"), ".")
}

// TestBucketNameStaysTheSame pins names of live buckets: a changed name
// makes a second bucket for each request.
func TestBucketNameStaysTheSame(t *testing.T) {
	for _, c := range []struct{ prefix, namespace, claim, want string }{
		{"camunda", "team-a", "orders", "camunda-team-a-orders-ae796c5f4c00991c"},
		{"prod-eu", "team.a", "orders.v2", "prod-eu-team-a-orders-v2-f992e3c9176a7752"},
		{
			strings.Repeat("p", 40),
			strings.Repeat("n", 63),
			"c",
			strings.Repeat("p", 40) + "-nnnnn-3d1ae7aac2e8d020",
		},
	} {
		assert.Equal(t, c.want, bucketName(c.prefix, c.namespace, c.claim))
	}
}

func TestBucketTagsHoldTheRequestTheUIDAndTheExtraTags(t *testing.T) {
	s := Settings{ExtraTags: awskit.ExtraTags{"team": "platform"}}
	ref := bucketRef{Namespace: "team-a", Claim: "orders", Request: "orders-backup", UID: "uid-1"}

	assert.Equal(t, map[string]string{
		awskit.TagNamespace:  "team-a",
		awskit.TagClaim:      "orders",
		awskit.TagRequest:    "orders-backup",
		awskit.TagRequestUID: "uid-1",
		"team":               "platform",
	}, bucketTags(s, ref))
}
