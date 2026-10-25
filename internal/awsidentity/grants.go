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
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// grantCounts are the numbers of the summary.
type grantCounts struct {
	applied, total, size int
}

// clusterGrant is a Grant of the cluster, with what the hub found for it.
type clusterGrant struct {
	grant   pluginsv1alpha1.Grant
	policy  grantPolicy
	matches cond
}

// syncGrants puts the allowed Grants of the cluster of req on role, removes
// the policies of Grants that are gone, and writes Applied, MatchesTemplate,
// and Ready of each Grant of the claim of req. It ignores a Grant that is
// being deleted, and one whose request is gone or belongs to another claim.
// It reports conflicted when a Grant changed after it was read, so that its
// status was not written.
func (p *Plugin) syncGrants(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	role awskit.Role,
) (grantCounts, bool, error) {
	grants, err := p.clusterGrants(ctx, req.Object)
	if err != nil {
		return grantCounts{}, false, err
	}

	current, err := awskit.InlinePolicies(ctx, p.clients.IAM, role.Name)
	if err != nil {
		return grantCounts{}, false, err
	}

	policies := make([]grantPolicy, 0, len(grants))
	for _, g := range grants {
		policies = append(policies, g.policy)
	}
	plan := planPolicies(current, policies)

	if err := p.applyPlan(ctx, req, role, current, grants, &plan); err != nil {
		return grantCounts{}, false, err
	}

	counts := grantCounts{total: len(grants), size: plan.size}
	conflicted := false
	for i := range grants {
		g := &grants[i]
		a := plan.applied[g.grant.Name]
		if a.status == metav1.ConditionTrue {
			counts.applied++
		}

		if !setGrantConditions(&g.grant, a, g.matches) {
			continue
		}

		err := p.client.Status().Update(ctx, &g.grant)
		switch {
		case apierrors.IsConflict(err) || apierrors.IsNotFound(err):
			conflicted = true
		case err != nil:
			return grantCounts{}, false, fmt.Errorf("writing the status of the Grant %s: %w", g.grant.Name, err)
		}
	}

	return counts, conflicted, nil
}

// clusterGrants returns the Grants of the cluster of request that belong to
// its claim, read fresh, sorted by creation time and then by name.
func (p *Plugin) clusterGrants(ctx context.Context, request *pluginsv1alpha1.PluginRequest) ([]clusterGrant, error) {
	var list pluginsv1alpha1.GrantList
	if err := p.reader.List(
		ctx,
		&list,
		client.InNamespace(request.Namespace),
		client.MatchingLabels{pluginsv1alpha1.LabelCluster: request.Spec.Target.Name},
	); err != nil {
		return nil, fmt.Errorf("listing the Grants of the cluster %s: %w", request.Spec.Target.Name, err)
	}

	slices.SortFunc(list.Items, func(a, b pluginsv1alpha1.Grant) int {
		return cmp.Or(a.CreationTimestamp.Compare(b.CreationTimestamp.Time), cmp.Compare(a.Name, b.Name))
	})

	var grants []clusterGrant
	for _, g := range list.Items {
		if !g.DeletionTimestamp.IsZero() {
			continue
		}

		owner, err := p.grantRequest(ctx, &g)
		if err != nil {
			return nil, err
		}
		if owner == nil || owner.Spec.ClaimRef != request.Spec.ClaimRef {
			continue
		}

		matches, err := p.matchesTemplate(ctx, &g, owner)
		if err != nil {
			return nil, err
		}

		grants = append(grants, clusterGrant{grant: g, policy: p.grantPolicy(&g), matches: matches})
	}

	return grants, nil
}

// grantRequest returns the request of the request label of grant, or nil
// when it does not exist.
func (p *Plugin) grantRequest(
	ctx context.Context,
	grant *pluginsv1alpha1.Grant,
) (*pluginsv1alpha1.PluginRequest, error) {
	name := grant.Labels[pluginsv1alpha1.LabelRequest]
	if name == "" {
		return nil, nil
	}

	var request pluginsv1alpha1.PluginRequest
	err := p.reader.Get(ctx, client.ObjectKey{Namespace: grant.Namespace, Name: name}, &request)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the request %s of the Grant %s: %w", name, grant.Name, err)
	}

	return &request, nil
}

// grantPolicy returns the policy of grant, or why the hub refuses it.
func (p *Plugin) grantPolicy(grant *pluginsv1alpha1.Grant) grantPolicy {
	gp := grantPolicy{grant: grant.Name, policy: policyName(grant.Name)}

	if grant.Spec.Format != pluginsv1alpha1.GrantFormatAWSIAMStatement {
		gp.refused = rejected(fmt.Sprintf(
			"The format %q is not %s",
			grant.Spec.Format,
			pluginsv1alpha1.GrantFormatAWSIAMStatement,
		))
		return gp
	}

	if grant.Spec.Fragment == nil {
		gp.refused = rejected("The Grant has no fragment")
		return gp
	}

	if gp.refused = p.ceiling.check(grant.Spec.Fragment.Raw); gp.refused != nil {
		return gp
	}

	doc, err := awskit.PolicyDocument(json.RawMessage(grant.Spec.Fragment.Raw))
	if err != nil {
		gp.refused = rejected(err.Error())
		return gp
	}
	gp.doc = doc

	return gp
}

// matchesTemplate compares the fragment of grant with its template in the
// registration of its registration label, rendered for request.
func (p *Plugin) matchesTemplate(
	ctx context.Context,
	grant *pluginsv1alpha1.Grant,
	request *pluginsv1alpha1.PluginRequest,
) (cond, error) {
	name := grant.Labels[pluginsv1alpha1.LabelRegistration]
	var registration pluginsv1alpha1.PluginRegistration
	err := p.reader.Get(ctx, client.ObjectKey{Name: name}, &registration)
	if apierrors.IsNotFound(err) {
		return templateChanged(fmt.Sprintf("The registration %q of the Grant does not exist", name)), nil
	}
	if err != nil {
		return cond{}, fmt.Errorf("reading the registration %s of the Grant %s: %w", name, grant.Name, err)
	}

	want, err := renderGrant(request, &registration, grant.Spec.Template)
	if err != nil {
		return templateChanged(err.Error()), nil
	}

	var got []byte
	if grant.Spec.Fragment != nil {
		got = grant.Spec.Fragment.Raw
	}
	if !awskit.SamePolicy(got, want) {
		return templateChanged(fmt.Sprintf(
			"The fragment no longer matches the template %s of the registration %s",
			grant.Spec.Template,
			name,
		)), nil
	}

	return cond{status: metav1.ConditionTrue, reason: v1.ReasonHealthy, message: matchesMessage}, nil
}

func templateChanged(message string) cond {
	return cond{status: metav1.ConditionFalse, reason: pluginsv1alpha1.ReasonTemplateChanged, message: message}
}

// applyPlan puts and removes the policies of plan on role, and records an
// event for each. A document that IAM refuses turns its Grant into
// Applied=False in plan, with the reason Rejected or OverSizeLimit.
func (p *Plugin) applyPlan(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	role awskit.Role,
	current map[string][]byte,
	grants []clusterGrant,
	plan *policyPlan,
) error {
	for _, g := range grants {
		doc, ok := plan.put[g.policy.policy]
		if !ok {
			continue
		}

		err := awskit.PutInlinePolicy(ctx, p.clients.IAM, role.Name, g.policy.policy, doc)
		var reason string
		switch {
		case err == nil:
			req.Event(
				corev1.EventTypeNormal,
				eventReasonPolicyApplied,
				eventActionUpdate,
				"Put the Grant %s on the IAM role %s as the inline policy %s",
				g.grant.Name,
				role.Name,
				g.policy.policy,
			)
			continue
		case awskit.IsMalformedPolicy(err):
			reason = pluginsv1alpha1.ReasonRejected
		case awskit.IsLimitExceeded(err):
			reason = pluginsv1alpha1.ReasonOverSizeLimit
		default:
			return err
		}

		old, onRole := current[g.policy.policy]
		plan.applied[g.grant.Name] = notApplied(reason, "IAM refused the policy: "+awskit.Message(err), onRole)
		plan.size += awskit.PolicySize(old) - awskit.PolicySize(doc)
	}

	for _, policy := range plan.remove {
		if err := awskit.DeleteInlinePolicy(ctx, p.clients.IAM, role.Name, policy); err != nil {
			return err
		}
		req.Event(
			corev1.EventTypeNormal,
			eventReasonPolicyRemoved,
			eventActionDelete,
			"Removed the inline policy %s of a Grant that is gone from the IAM role %s",
			policy,
			role.Name,
		)
	}

	return nil
}
