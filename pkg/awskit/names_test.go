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

func TestRequestTagsNameTheNamespaceTheClaimAndTheRequest(t *testing.T) {
	assert.Equal(t, map[string]string{
		"camunda.io/namespace": "team-a",
		"camunda.io/claim":     "orders",
		"camunda.io/request":   "orders-identity",
	}, RequestTags("team-a", "orders", "orders-identity"))
}
