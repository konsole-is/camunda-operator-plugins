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

package awsconnectorsrole

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	corev1 "k8s.io/api/core/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// reconcileRole keeps the role of the claim of req, trusted by the
// Connectors ServiceAccount only, with the inline policy and the managed
// policies of the parameters, then deletes the old roles of the status once
// the outputs name the role. It is Pending when the request names no
// ServiceAccount, Pending with no AWS call when the inline policy is over the
// size limit, and Pending when the role of its name belongs to another
// request. A Pending result keeps the old roles of the status.
func (p *Plugin) reconcileRole(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	request := req.Object
	sas := trustedServiceAccounts(request.Spec.Target)
	if len(sas) == 0 {
		return p.reconcileRoleWithoutServiceAccount(ctx, req)
	}

	status := request.Status.Resources
	doc, size, err := inlinePolicyDocument(req.Parameters.InlinePolicy)
	if err != nil {
		// No role is read, so each role of the status stays.
		return pluginkit.Result{
			Pending: &pluginkit.Waiting{
				Reason:  pluginsv1alpha1.ReasonInvalidParameters,
				Message: "spec.parameters.inlinePolicy: " + err.Error(),
			},
			Resources: oldRoleRows(status, ""),
		}, nil
	}

	role, err := p.ensureRole(ctx, req, p.roleName(request), sas)
	if res, ok := roleNameInUse(err, status); ok {
		return res, nil
	}
	if err != nil {
		return pluginkit.Result{}, err
	}

	// Like the trust policy of IRSA, so that a former ServiceAccount loses its
	// access before a policy step can fail.
	associations, err := p.ensureAssociations(ctx, req, sas, role.ARN)
	if err != nil {
		return pluginkit.Result{}, err
	}

	if err := p.ensureInlinePolicy(ctx, req, role, doc); err != nil {
		return pluginkit.Result{}, err
	}

	wanted := wantedPolicies(req.Parameters)
	if err := p.ensureAttachedPolicies(ctx, req, role, wanted); err != nil {
		return pluginkit.Result{}, err
	}

	old, err := p.retireOldRoles(ctx, req, role)
	if err != nil {
		return pluginkit.Result{}, err
	}
	resources := append([]pluginsv1alpha1.ResourceRef{roleResource(role, request)}, associations...)
	resources = append(resources, old...)

	consoleURL, err := awskit.RoleConsoleURL(role.ARN)
	if err != nil {
		return pluginkit.Result{}, err
	}

	res := pluginkit.Result{
		Outputs:   awskit.IdentityOutputs(p.mode, role.ARN),
		Summary:   summary(role, len(wanted), size, p.boundary),
		Links:     []pluginsv1alpha1.Link{{Name: "IAM role", URL: consoleURL}},
		Resources: resources,
	}
	if len(old) > 0 {
		res.RequeueAfter = oldRoleRequeue
	}
	return res, nil
}

// roleNameInUse returns the Pending result of a request whose role belongs
// to another request, when err is an awskit.RoleInUseError. The result keeps
// the old roles of the rows of status.
func roleNameInUse(err error, status []pluginsv1alpha1.ResourceRef) (pluginkit.Result, bool) {
	var inUse awskit.RoleInUseError
	if !errors.As(err, &inUse) {
		return pluginkit.Result{}, false
	}

	return pluginkit.Result{
		Pending:      &pluginkit.Waiting{Reason: ReasonRoleNameInUse, Message: inUse.Error()},
		Resources:    oldRoleRows(status, inUse.Name),
		RequeueAfter: roleInUseRequeue,
	}, true
}

func roleResource(role awskit.Role, request *pluginsv1alpha1.PluginRequest) pluginsv1alpha1.ResourceRef {
	return pluginsv1alpha1.ResourceRef{
		Kind:     kindIAMRole,
		Name:     role.Name,
		ID:       role.ARN,
		Role:     "AWS identity of the Connectors of the cluster " + request.Spec.Target.Name,
		OnOrphan: pluginsv1alpha1.OnOrphanKept,
	}
}

// reconcileRoleWithoutServiceAccount makes the role of the claim of req, when
// it exists, trust nobody, and deletes the associations of the request.
// Before the role of its current name exists, it does this to the old role
// that the outputs name. It creates no role and deletes no old role, because
// the outputs can still name one. The Pending result keeps the role and the
// old roles of the status.
func (p *Plugin) reconcileRoleWithoutServiceAccount(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	current := p.roleName(req.Object)
	status := req.Object.Status.Resources
	arn, err := p.liveRoleARN(ctx, current)
	if err != nil {
		return pluginkit.Result{}, err
	}

	name := current
	if arn == "" {
		old, err := p.activeOldRole(ctx, req, current)
		if err != nil {
			return pluginkit.Result{}, err
		}
		name, arn = old.Name, old.ARN
	}

	res := pluginkit.Result{Pending: missingServiceAccount(), Resources: oldRoleRows(status, current)}
	if arn != "" {
		role, err := p.ensureRole(ctx, req, name, nil)
		if inUse, ok := roleNameInUse(err, status); ok {
			return inUse, nil
		}
		if err != nil {
			return pluginkit.Result{}, err
		}
		res.Resources = append(
			[]pluginsv1alpha1.ResourceRef{roleResource(role, req.Object)},
			oldRoleRows(status, role.Name)...)
	}

	if err := p.deleteAssociations(ctx, req); err != nil {
		return pluginkit.Result{}, err
	}
	return res, nil
}

func missingServiceAccount() *pluginkit.Waiting {
	return &pluginkit.Waiting{
		Reason:  ReasonMissingServiceAccount,
		Message: "The request names no ServiceAccount in spec.target.serviceAccountName",
	}
}

// ensureRole keeps the role name of the claim of req, trusted by sas, and
// records an event when it created or updated the role.
func (p *Plugin) ensureRole(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	name string,
	sas []string,
) (awskit.Role, error) {
	request := req.Object
	trust, err := awskit.TrustPolicy(p.mode, p.provider, request.Namespace, sas)
	if err != nil {
		return awskit.Role{}, err
	}

	role, change, err := awskit.EnsureRole(ctx, p.clients.IAM, awskit.RoleSpec{
		Name:                name,
		Description:         "Connectors of the Camunda cluster " + request.Namespace + "/" + request.Spec.ClaimRef.Name,
		TrustPolicy:         trust,
		PermissionsBoundary: p.boundary,
		Tags:                p.roleTags(request),
	})
	if err != nil {
		return awskit.Role{}, err
	}

	switch {
	case change.Created:
		req.Event(
			corev1.EventTypeNormal,
			eventReasonRoleCreated,
			eventActionCreate,
			"Created the IAM role %s",
			role.Name,
		)
	case len(change.Updated) > 0:
		req.Event(
			corev1.EventTypeNormal,
			eventReasonRoleUpdated,
			eventActionUpdate,
			"Updated the %s of the IAM role %s",
			strings.Join(change.Updated, ", "),
			role.Name,
		)
	}

	return role, nil
}

// roleName is the role of the claim of request. The OIDC issuer names one
// EKS cluster, and the region and the name of an EKS cluster name one too.
func (p *Plugin) roleName(request *pluginsv1alpha1.PluginRequest) string {
	scope := p.provider.Issuer
	if p.mode == awskit.ModePodIdentity {
		scope = p.region + "/" + p.eksCluster
	}
	return awskit.RoleName(p.prefix, scope, request.Namespace, request.Spec.ClaimRef.Name)
}

// ensureInlinePolicy makes the inline policy inlinePolicyName of role doc,
// or deletes it when doc is nil, and records an event when it changed it.
func (p *Plugin) ensureInlinePolicy(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	role awskit.Role,
	doc []byte,
) error {
	live, err := p.inlinePolicy(ctx, role.Name)
	if err != nil {
		return err
	}

	switch {
	case doc == nil && live == nil:
		return nil
	case doc == nil:
		if err := awskit.DeleteInlinePolicy(ctx, p.clients.IAM, role.Name, inlinePolicyName); err != nil {
			return err
		}
		req.Event(
			corev1.EventTypeNormal,
			eventReasonInlinePolicyDeleted,
			eventActionDelete,
			"Deleted the inline policy %s of the IAM role %s",
			inlinePolicyName,
			role.Name,
		)
	case !awskit.SamePolicy(live, doc):
		if err := awskit.PutInlinePolicy(ctx, p.clients.IAM, role.Name, inlinePolicyName, doc); err != nil {
			return err
		}
		req.Event(
			corev1.EventTypeNormal,
			eventReasonInlinePolicyUpdated,
			eventActionUpdate,
			"Updated the inline policy %s of the IAM role %s",
			inlinePolicyName,
			role.Name,
		)
	}

	return nil
}

// inlinePolicy returns the decoded inline policy inlinePolicyName of the
// role, or nil when it does not exist.
func (p *Plugin) inlinePolicy(ctx context.Context, role string) ([]byte, error) {
	out, err := p.clients.IAM.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
		RoleName:   aws.String(role),
		PolicyName: aws.String(inlinePolicyName),
	})
	if awskit.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the inline policy %s of the IAM role %s: %w", inlinePolicyName, role, err)
	}

	return awskit.DecodePolicy(aws.ToString(out.PolicyDocument))
}

// ensureAttachedPolicies makes the managed policies of role exactly arns,
// and records an event for each policy that it attached or detached.
func (p *Plugin) ensureAttachedPolicies(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	role awskit.Role,
	arns []string,
) error {
	change, err := awskit.EnsureAttachedPolicies(ctx, p.clients.IAM, role.Name, arns)
	for _, arn := range change.Detached {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonPolicyDetached,
			eventActionUpdate,
			"Detached the policy %s from the IAM role %s",
			arn,
			role.Name,
		)
	}
	for _, arn := range change.Attached {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonPolicyAttached,
			eventActionUpdate,
			"Attached the policy %s to the IAM role %s",
			arn,
			role.Name,
		)
	}

	return err
}

// deleteRole deletes the associations of req, and each role of req with its
// policies: the role of its current name and the roles of its status.
func (p *Plugin) deleteRole(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	if err := p.deleteAssociations(ctx, req); err != nil {
		return err
	}

	current := p.roleName(req.Object)
	old := oldRoleRows(req.Object.Status.Resources, current)
	names := make([]string, 0, len(old)+1)
	names = append(names, current)
	for _, row := range old {
		names = append(names, row.Name)
	}

	for _, name := range names {
		if err := p.deleteNamedRole(ctx, req, name, ""); err != nil {
			return err
		}
	}
	return nil
}

// liveRoleARN returns the ARN of the role name, or "" when it does not exist.
func (p *Plugin) liveRoleARN(ctx context.Context, name string) (string, error) {
	out, err := p.clients.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if awskit.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the IAM role %s: %w", name, err)
	}

	return aws.ToString(out.Role.Arn), nil
}
