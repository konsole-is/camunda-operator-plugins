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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleNameFitsIAMForLongNamespacesAndClaims(t *testing.T) {
	name := RoleName(
		"camunda",
		"oidc.eks.eu-west-1.amazonaws.com/id/ABC",
		strings.Repeat("n", 63),
		strings.Repeat("c", 253),
	)

	assert.LessOrEqual(t, len(name), 64)
	assert.Regexp(t, `^camunda-[0-9a-f]{16}$`, name)
}

func TestRoleNameDiffersForEachScopeNamespaceAndClaim(t *testing.T) {
	assert.Equal(t, RoleName("camunda", "eks-1", "team-a", "orders"), RoleName("camunda", "eks-1", "team-a", "orders"))
	assert.NotEqual(
		t,
		RoleName("camunda", "eks-1", "team-a", "orders"),
		RoleName("camunda", "eks-2", "team-a", "orders"),
	)
	assert.NotEqual(
		t,
		RoleName("camunda", "eks-1", "team-a", "orders"),
		RoleName("camunda", "eks-1", "team-b", "orders"),
	)
	assert.NotEqual(t, RoleName("camunda", "eks-1", "a-b", "c"), RoleName("camunda", "eks-1", "a", "b-c"))
	assert.NotEqual(t, RoleName("camunda", "e", "ks/a", "b"), RoleName("camunda", "e/ks", "a", "b"))
	assert.NotEqual(
		t,
		RoleName("camunda", "eks-1", "team-a", "orders"),
		RoleName("camunda-connectors", "eks-1", "team-a", "orders"),
	)
}

func TestFitNameKeepsAShortNameAndCutsALongOne(t *testing.T) {
	assert.Equal(t, "alias/camunda/team-a/orders", FitName("alias/camunda/team-a/orders", 256))

	cut := FitName(strings.Repeat("x", 300), 256)

	assert.Len(t, cut, 256)
	assert.NotEqual(t, cut, FitName(strings.Repeat("x", 299)+"y", 256))
}

func TestBucketNameIsAValidBucketNameOfTheParts(t *testing.T) {
	valid := `^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`

	assert.Regexp(t, `^team-a-orders-backup-storage-[0-9a-f]{16}$`, BucketName("team-a", "orders-backup-storage"))
	assert.Equal(t, BucketName("team-a", "orders"), BucketName("team-a", "orders"), "the name is stable")

	long := BucketName(strings.Repeat("n", 63), strings.Repeat("r", 63))
	assert.Len(t, long, 63)
	assert.Regexp(t, valid, long)

	dotted := BucketName("team-a", "orders.v2")
	assert.Regexp(t, valid, dotted)
	assert.NotContains(t, dotted, ".")

	// A cut that ends on a hyphen must not leave two hyphens before the hash.
	assert.Regexp(t, valid, BucketName(strings.Repeat("n", 45), "r-request"))
	assert.NotContains(t, BucketName(strings.Repeat("n", 45), "r-request"), "--")
}

func TestBucketNameKeepsTwoListsOfPartsApart(t *testing.T) {
	assert.NotEqual(t, BucketName("a-b", "c"), BucketName("a", "b-c"))
	assert.NotEqual(t, BucketName("p", "a-b", "c"), BucketName("p-a", "b", "c"))
	assert.NotEqual(
		t,
		BucketName(strings.Repeat("n", 63), strings.Repeat("r", 62)+"a"),
		BucketName(strings.Repeat("n", 63), strings.Repeat("r", 62)+"b"),
		"two names that differ after the cut",
	)
	assert.NotEqual(
		t,
		BucketName(strings.Repeat("n", 56)+"a", "orders-119-backup-storage"),
		BucketName(strings.Repeat("n", 56)+"b", "orders-295-backup-storage"),
		"two names whose hashes share their first 6 hex characters",
	)
}

func TestCheckRolePrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		ok     bool
	}{
		{name: "plain", prefix: "camunda", ok: true},
		{name: "run prefix", prefix: "cpo-ci-42-conn", ok: true},
		{name: "every allowed mark", prefix: "a+b=c,d.e@f_g", ok: true},
		{name: "longest", prefix: strings.Repeat("p", 47), ok: true},
		{name: "too long", prefix: strings.Repeat("p", 48)},
		{name: "empty", prefix: ""},
		{name: "slash", prefix: "team/conn"},
		{name: "space", prefix: "team conn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRolePrefix(tt.prefix)
			if !tt.ok {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.LessOrEqual(t, len(RoleName(tt.prefix, "s", "n", "c")), 64)
		})
	}

	assert.Len(t, RoleName(strings.Repeat("p", 47), "s", "n", "c"), 64)
	assert.ErrorContains(t, CheckRolePrefix(strings.Repeat("p", 48)), "47")
}

func TestCheckAliasPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		ok     bool
	}{
		{name: "plain", prefix: "camunda", ok: true},
		{name: "run prefix", prefix: "cpo-ci-42", ok: true},
		{name: "path", prefix: "team/kms_keys", ok: true},
		{name: "longest", prefix: strings.Repeat("p", 128), ok: true},
		{name: "too long", prefix: strings.Repeat("p", 129)},
		{name: "empty", prefix: ""},
		{name: "aws", prefix: "aws"},
		{name: "under aws", prefix: "aws/keys"},
		{name: "starts with the letters aws", prefix: "awskeys", ok: true},
		{name: "alias prefix", prefix: "alias/camunda"},
		{name: "leading slash", prefix: "/camunda"},
		{name: "trailing slash", prefix: "camunda/"},
		{name: "dot", prefix: "camunda.keys"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ok {
				assert.NoError(t, CheckAliasPrefix(tt.prefix))
			} else {
				assert.Error(t, CheckAliasPrefix(tt.prefix))
			}
		})
	}

	assert.ErrorContains(t, CheckAliasPrefix(strings.Repeat("p", 129)), "128")
}
