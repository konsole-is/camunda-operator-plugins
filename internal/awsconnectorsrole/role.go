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
// policies of the parameters. It is Pending, with no AWS call, when the
// request names no ServiceAccount or when the inline policy is over the size
// limit.
func (p *Plugin) reconcileRole(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	request := req.Object
	sas := trustedServiceAccounts(request.Spec.Target)
	if len(sas) == 0 {
		return pluginkit.Result{Pending: missingServiceAccount()}, nil
	}

	doc, size, err := inlinePolicyDocument(req.Parameters.InlinePolicy)
	if err != nil {
		return pluginkit.Result{Pending: &pluginkit.Waiting{
			Reason:  pluginsv1alpha1.ReasonInvalidParameters,
			Message: "spec.parameters.inlinePolicy: " + err.Error(),
		}}, nil
	}

	role, err := p.ensureRole(ctx, req, sas)
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

	associations, err := p.ensureAssociations(ctx, req, sas, role.ARN)
	if err != nil {
		return pluginkit.Result{}, err
	}
	resources := append([]pluginsv1alpha1.ResourceRef{{
		Kind:     kindIAMRole,
		Name:     role.Name,
		ID:       role.ARN,
		Role:     "AWS identity of the Connectors of the cluster " + request.Spec.Target.Name,
		OnOrphan: pluginsv1alpha1.OnOrphanKept,
	}}, associations...)

	consoleURL, err := awskit.RoleConsoleURL(role.ARN)
	if err != nil {
		return pluginkit.Result{}, err
	}

	return pluginkit.Result{
		Outputs:   awskit.IdentityOutputs(p.mode, role.ARN),
		Summary:   summary(role, len(wanted), size, p.boundary),
		Links:     []pluginsv1alpha1.Link{{Name: "IAM role", URL: consoleURL}},
		Resources: resources,
	}, nil
}

func missingServiceAccount() *pluginkit.Waiting {
	return &pluginkit.Waiting{
		Reason:  ReasonMissingServiceAccount,
		Message: "The request names no ServiceAccount in spec.target.serviceAccountName",
	}
}

// ensureRole keeps the role of the claim of req, trusted by sas, and records
// an event when it created or updated the role.
func (p *Plugin) ensureRole(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	sas []string,
) (awskit.Role, error) {
	request := req.Object
	trust, err := awskit.TrustPolicy(p.mode, p.provider, request.Namespace, sas)
	if err != nil {
		return awskit.Role{}, err
	}

	role, change, err := awskit.EnsureRole(ctx, p.clients.IAM, awskit.RoleSpec{
		Name:                p.roleName(request),
		Description:         "Connectors of the Camunda cluster " + request.Namespace + "/" + request.Spec.ClaimRef.Name,
		TrustPolicy:         trust,
		PermissionsBoundary: p.boundary,
		Tags:                p.tags(request),
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

// deleteRole deletes the associations of the role of the claim of req, its
// policies, and the role.
func (p *Plugin) deleteRole(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	if err := p.deleteOwnedAssociations(ctx, req); err != nil {
		return err
	}

	roleName := p.roleName(req.Object)
	deleted, err := awskit.DeleteRole(ctx, p.clients.IAM, roleName)
	if err != nil {
		return err
	}

	if deleted {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonRoleDeleted,
			eventActionDelete,
			"Deleted the IAM role %s",
			roleName,
		)
	}
	return nil
}

// deleteOwnedAssociations deletes, with Pod Identity, the associations of the
// claim of req to its role. With IRSA it makes no AWS call.
func (p *Plugin) deleteOwnedAssociations(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) error {
	if p.mode != awskit.ModePodIdentity {
		return nil
	}

	arn, err := p.ownedRoleARN(ctx, req)
	if err != nil {
		return err
	}
	return p.deleteAssociations(ctx, req, arn)
}

// ownedRoleARN returns the ARN of the role of the claim of req. When the
// role is gone, it returns the principal output, because the associations of
// a role that someone deleted by hand stay, or "" when there is none.
func (p *Plugin) ownedRoleARN(ctx context.Context, req pluginkit.Request[Params, map[string]string]) (string, error) {
	name := p.roleName(req.Object)
	out, err := p.clients.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if awskit.IsNotFound(err) {
		return req.Object.Status.Outputs[awskit.OutputPrincipal], nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the IAM role %s: %w", name, err)
	}

	return aws.ToString(out.Role.Arn), nil
}
