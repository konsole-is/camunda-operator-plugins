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
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// The messages of the conditions of a Grant that is in order.
const (
	appliedMessage = "The role has the statement as an inline policy"
	matchesMessage = "The fragment matches its grant template"
)

// keptMessage ends the message of a refused Grant whose statement stays on
// the role.
const keptMessage = "The role keeps the statement that it had before."

// grantPolicy is one Grant of the cluster, as the plan reads it.
type grantPolicy struct {
	grant  string
	policy string
	// doc is the policy document of the fragment. It is nil when refused is
	// set.
	doc     []byte
	refused *refusal
}

// cond is the status, the reason, and the message of one condition of a
// Grant.
type cond struct {
	status          metav1.ConditionStatus
	reason, message string
}

// policyPlan is what the hub writes on a role.
type policyPlan struct {
	// put maps a policy name to its new document.
	put map[string][]byte
	// remove holds the policies of Grants that are gone, sorted.
	remove []string
	// applied maps a Grant name to its Applied condition.
	applied map[string]cond
	// size is the size of every inline policy of the role after the plan.
	size int
}

// planPolicies returns the plan for a role with the inline policies current
// and grants, in the order of grants. A Grant that is refused, or that would
// take the inline policies over awskit.MaxInlinePolicySize, is not applied, and the statement
// that the role had for it stays. Only a policy of the hub whose Grant is
// not in grants is removed. A policy without policyPrefix is never touched,
// and counts towards the limit.
func planPolicies(current map[string][]byte, grants []grantPolicy) policyPlan {
	plan := policyPlan{put: map[string][]byte{}, applied: map[string]cond{}}
	live := map[string]bool{}
	for _, g := range grants {
		live[g.policy] = true
	}

	for name, doc := range current {
		if !live[name] && strings.HasPrefix(name, policyPrefix) {
			plan.remove = append(plan.remove, name)
			continue
		}
		plan.size += awskit.PolicySize(doc)
	}
	slices.Sort(plan.remove)

	// Two Grants whose names fit to one policy name would write that policy in
	// turn, so the Grant with the first name holds it.
	holders := map[string]string{}
	for _, g := range grants {
		if h, ok := holders[g.policy]; g.refused == nil && (!ok || g.grant < h) {
			holders[g.policy] = g.grant
		}
	}

	for _, g := range grants {
		old, onRole := current[g.policy]
		switch {
		case g.refused != nil:
			plan.applied[g.grant] = notApplied(g.refused.reason, g.refused.message, onRole)
		case holders[g.policy] != g.grant:
			plan.applied[g.grant] = notApplied(pluginsv1alpha1.ReasonRejected, fmt.Sprintf(
				"The IAM policy name %s is also the name of the policy of the Grant %s",
				g.policy,
				holders[g.policy],
			), false)
		case onRole && awskit.SamePolicy(old, g.doc):
			plan.applied[g.grant] = isApplied()
		default:
			next := plan.size - awskit.PolicySize(old) + awskit.PolicySize(g.doc)
			if next > awskit.MaxInlinePolicySize {
				plan.applied[g.grant] = notApplied(pluginsv1alpha1.ReasonOverSizeLimit, fmt.Sprintf(
					"The inline policies of the role would have %d characters, over the limit of %d",
					next,
					awskit.MaxInlinePolicySize,
				), onRole)
				continue
			}

			plan.put[g.policy], plan.size = g.doc, next
			plan.applied[g.grant] = isApplied()
		}
	}

	return plan
}

func isApplied() cond {
	return cond{status: metav1.ConditionTrue, reason: v1.ReasonHealthy, message: appliedMessage}
}

func notApplied(reason, message string, onRole bool) cond {
	if onRole {
		message = strings.TrimSuffix(message, ".") + ". " + keptMessage
	}
	return cond{status: metav1.ConditionFalse, reason: reason, message: message}
}
