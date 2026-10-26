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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// policyOfSize returns a compact document of n counted characters.
func policyOfSize(t *testing.T, n int) []byte {
	doc := []byte(`{"a":"` + strings.Repeat("x", n-8) + `"}`)
	require.Equal(t, n, awskit.PolicySize(doc))
	return doc
}

func TestPlanPoliciesAppliesInOrderUntilTheLimit(t *testing.T) {
	a, b, c := policyOfSize(t, 6000), policyOfSize(t, 5000), policyOfSize(t, 4000)

	plan := planPolicies(nil, []grantPolicy{
		{grant: "a", policy: "grant-a", doc: a},
		{grant: "b", policy: "grant-b", doc: b},
		{grant: "c", policy: "grant-c", doc: c},
	})

	assert.Equal(t, map[string][]byte{"grant-a": a, "grant-c": c}, plan.put)
	assert.Equal(t, metav1.ConditionTrue, plan.applied["a"].status)
	assert.Equal(t, metav1.ConditionFalse, plan.applied["b"].status)
	assert.Equal(t, pluginsv1alpha1.ReasonOverSizeLimit, plan.applied["b"].reason)
	assert.Contains(t, plan.applied["b"].message, "11000")
	assert.Equal(t, metav1.ConditionTrue, plan.applied["c"].status)
	assert.Equal(t, 10000, plan.size)
}

func TestPlanPoliciesKeepsTheAppliedStatementOfARefusedChange(t *testing.T) {
	old := policyOfSize(t, 100)

	plan := planPolicies(map[string][]byte{"grant-a": old}, []grantPolicy{{
		grant:   "a",
		policy:  "grant-a",
		refused: &refusal{reason: pluginsv1alpha1.ReasonOverCeiling, message: "Action iam:* is outside"},
	}})

	assert.Empty(t, plan.put)
	assert.Empty(t, plan.remove)
	assert.Equal(t, pluginsv1alpha1.ReasonOverCeiling, plan.applied["a"].reason)
	assert.Contains(t, plan.applied["a"].message, "Action iam:* is outside")
	assert.Contains(t, plan.applied["a"].message, "The role keeps the statement that it had before.")
	assert.Equal(t, 100, plan.size)
}

func TestPlanPoliciesSaysNothingAboutAKeptStatementOfANewGrant(t *testing.T) {
	plan := planPolicies(nil, []grantPolicy{{
		grant:   "a",
		policy:  "grant-a",
		refused: &refusal{reason: pluginsv1alpha1.ReasonRejected, message: "m"},
	}})

	assert.Equal(t, "m", plan.applied["a"].message)
}

func TestPlanPoliciesKeepsTheAppliedStatementOverTheLimit(t *testing.T) {
	old, grown := policyOfSize(t, 6000), policyOfSize(t, 11000)

	plan := planPolicies(map[string][]byte{"grant-a": old}, []grantPolicy{
		{grant: "a", policy: "grant-a", doc: grown},
	})

	assert.Empty(t, plan.put)
	assert.Equal(t, pluginsv1alpha1.ReasonOverSizeLimit, plan.applied["a"].reason)
	assert.Contains(t, plan.applied["a"].message, "The role keeps the statement that it had before.")
	assert.Equal(t, 6000, plan.size)
}

func TestPlanPoliciesReplacesAChangedStatementInsideTheLimit(t *testing.T) {
	old, changed := policyOfSize(t, 6000), policyOfSize(t, 7000)

	plan := planPolicies(map[string][]byte{"grant-a": old}, []grantPolicy{
		{grant: "a", policy: "grant-a", doc: changed},
	})

	assert.Equal(t, map[string][]byte{"grant-a": changed}, plan.put)
	assert.Equal(t, 7000, plan.size)
}

func TestPlanPoliciesRemovesThePolicyOfAGrantThatIsGone(t *testing.T) {
	plan := planPolicies(map[string][]byte{
		"grant-old": policyOfSize(t, 50),
		"other":     policyOfSize(t, 60),
	}, nil)

	assert.Equal(t, []string{"grant-old"}, plan.remove, "a policy without the prefix is not the hub's")
	assert.Equal(t, 60, plan.size)
}

func TestPlanPoliciesLeavesAnEqualPolicyAlone(t *testing.T) {
	plan := planPolicies(map[string][]byte{"grant-a": []byte(`{"a":1, "b":2}`)}, []grantPolicy{
		{grant: "a", policy: "grant-a", doc: []byte(`{"b":2,"a":1}`)},
	})

	assert.Empty(t, plan.put)
	assert.Equal(t, cond{status: metav1.ConditionTrue, reason: "Healthy", message: appliedMessage}, plan.applied["a"])
}

func TestPlanPoliciesGivesAnAliasedPolicyNameToTheFirstGrant(t *testing.T) {
	long := strings.Repeat("a", 200)
	alias := strings.TrimPrefix(policyName(long), policyPrefix)
	require.Equal(t, policyName(long), policyName(alias))
	require.Less(t, alias, long)
	first, second := policyOfSize(t, 100), policyOfSize(t, 200)

	plan := planPolicies(nil, []grantPolicy{
		{grant: long, policy: policyName(long), doc: second},
		{grant: alias, policy: policyName(alias), doc: first},
	})

	assert.Equal(t, map[string][]byte{policyName(alias): first}, plan.put)
	assert.Equal(t, metav1.ConditionTrue, plan.applied[alias].status)
	assert.Equal(t, metav1.ConditionFalse, plan.applied[long].status)
	assert.Equal(t, pluginsv1alpha1.ReasonRejected, plan.applied[long].reason)
	assert.Contains(t, plan.applied[long].message, alias)

	// The next pass finds the policy of the holder and writes nothing.
	again := planPolicies(plan.put, []grantPolicy{
		{grant: long, policy: policyName(long), doc: second},
		{grant: alias, policy: policyName(alias), doc: first},
	})
	assert.Empty(t, again.put)
}
