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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// ReasonMissingServiceAccount means that a request names no ServiceAccount
// in spec.target, so its role would trust nobody.
const ReasonMissingServiceAccount = "MissingServiceAccount"

// The events that the hub records on a request.
const (
	eventReasonRoleCreated        = "RoleCreated"
	eventReasonRoleUpdated        = "RoleUpdated"
	eventReasonRoleDeleted        = "RoleDeleted"
	eventReasonAssociationCreated = "AssociationCreated"
	eventReasonAssociationUpdated = "AssociationUpdated"
	eventReasonAssociationDeleted = "AssociationDeleted"
	eventReasonPolicyApplied      = "PolicyApplied"
	eventReasonPolicyRemoved      = "PolicyRemoved"
	eventActionCreate             = "Create"
	eventActionUpdate             = "Update"
	eventActionDelete             = "Delete"
)

// The resource kinds of status.resources.
const (
	kindIAMRole                = "IAMRole"
	kindPodIdentityAssociation = "PodIdentityAssociation"
)

// conflictRequeue is the wait before a request reconciles again after the
// status write of one of its Grants hit a newer copy.
const conflictRequeue = time.Second

// Params are the parameters of a request. The registrations declare none.
type Params struct{}

// Plugin serves cloud identity requests. Each request is one cluster: the
// hub keeps one IAM role for its claim, trusted by the ServiceAccounts of
// spec.target, and puts the Grants of that cluster on it.
type Plugin struct {
	mode       awskit.Mode
	provider   awskit.OIDCProvider
	boundary   string
	ceiling    Ceiling
	eksCluster string
	clients    awskit.Clients
	// client writes the status of Grants.
	client client.Client
	// reader reads the Grants and the requests and registrations that they
	// name, fresh, so that a status write carries a current resourceVersion.
	reader client.Reader
}

// Reconcile keeps the role of the request, its trust, and with Pod Identity
// its associations, then puts the allowed Grants of the cluster on the role
// and writes their conditions. It is Pending with the reason
// MissingServiceAccount when spec.target names no ServiceAccount. A failed
// AWS call is an error, except a policy document that IAM refuses, which
// sets Applied=False on its Grant.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	request := req.Object
	sas := serviceAccounts(request.Spec.Target)
	if len(sas) == 0 {
		return p.reconcileWithoutServiceAccounts(ctx, req)
	}

	role, err := p.ensureRole(ctx, req, sas)
	if err != nil {
		return pluginkit.Result{}, err
	}

	resources := []pluginsv1alpha1.ResourceRef{roleResource(role, request)}
	if p.mode == awskit.ModePodIdentity {
		associations, err := p.ensureAssociations(ctx, req, sas, role)
		if err != nil {
			return pluginkit.Result{}, err
		}

		for _, a := range associations {
			resources = append(resources, pluginsv1alpha1.ResourceRef{
				Kind:     kindPodIdentityAssociation,
				Name:     a.ServiceAccount,
				ID:       a.ID,
				Location: p.eksCluster,
				Role:     "Gives the ServiceAccount " + a.ServiceAccount + " the role",
				OnOrphan: pluginsv1alpha1.OnOrphanDeleted,
			})
		}
	}

	counts, conflicted, err := p.syncGrants(ctx, req, role)
	if err != nil {
		return pluginkit.Result{}, err
	}

	outputs, err := p.outputs(role)
	if err != nil {
		return pluginkit.Result{}, err
	}

	consoleURL, err := awskit.RoleConsoleURL(role.ARN)
	if err != nil {
		return pluginkit.Result{}, err
	}

	res := pluginkit.Result{
		Outputs:   outputs,
		Summary:   summary(role, p.boundary, counts.applied, counts.total, counts.size),
		Links:     []pluginsv1alpha1.Link{{Name: "IAM role", URL: consoleURL}},
		Resources: resources,
	}
	if conflicted {
		res.RequeueAfter = conflictRequeue
	}

	return res, nil
}

func roleResource(role awskit.Role, request *pluginsv1alpha1.PluginRequest) pluginsv1alpha1.ResourceRef {
	return pluginsv1alpha1.ResourceRef{
		Kind:     kindIAMRole,
		Name:     role.Name,
		ID:       role.ARN,
		Role:     "Cloud identity of the cluster " + request.Spec.Target.Name,
		OnOrphan: pluginsv1alpha1.OnOrphanKept,
	}
}

// reconcileWithoutServiceAccounts makes the role of req, when it has one,
// trust nobody, and keeps its Grants in sync, because credentials that STS
// gave before stay valid.
func (p *Plugin) reconcileWithoutServiceAccounts(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	res := pluginkit.Result{Pending: &pluginkit.Waiting{
		Reason:  ReasonMissingServiceAccount,
		Message: "The request names no ServiceAccount in spec.target",
	}}

	arn, err := p.liveRoleARN(ctx, p.roleName(req.Object))
	if err != nil {
		return res, err
	}
	if arn == "" {
		// The associations of a role that someone deleted by hand still hold
		// the ServiceAccounts.
		return res, p.deleteAssociations(ctx, req, p.roleName(req.Object))
	}

	role, err := p.ensureRole(ctx, req, nil)
	if err != nil {
		return pluginkit.Result{}, err
	}

	if p.mode == awskit.ModePodIdentity {
		if _, err := p.ensureAssociations(ctx, req, nil, role); err != nil {
			return pluginkit.Result{}, err
		}
	}

	_, conflicted, err := p.syncGrants(ctx, req, role)
	if err != nil {
		return pluginkit.Result{}, err
	}

	res.Resources = []pluginsv1alpha1.ResourceRef{roleResource(role, req.Object)}
	if conflicted {
		res.RequeueAfter = conflictRequeue
	}
	return res, nil
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

// ensureRole keeps the role of the claim of req, trusted by sas, or by nobody
// when sas is empty, and records an event when it created or updated the
// role.
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
		Description:         "Camunda cluster " + request.Namespace + "/" + request.Spec.ClaimRef.Name,
		TrustPolicy:         trust,
		PermissionsBoundary: p.boundary,
		Tags:                awskit.RequestTags(request.Namespace, request.Spec.ClaimRef.Name, request.Name),
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
// EKS cluster, and the EKS cluster name names one in its region.
func (p *Plugin) roleName(request *pluginsv1alpha1.PluginRequest) string {
	scope := p.provider.Issuer
	if p.mode == awskit.ModePodIdentity {
		scope = p.eksCluster
	}
	return awskit.RoleName(rolePrefix, scope, request.Namespace, request.Spec.ClaimRef.Name)
}

// ensureAssociations keeps one Pod Identity association of role for each of
// sas, and records an event for each change.
func (p *Plugin) ensureAssociations(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	sas []string,
	role awskit.Role,
) ([]awskit.Association, error) {
	request := req.Object
	associations, change, err := awskit.EnsureAssociations(
		ctx,
		p.clients.EKS,
		p.eksCluster,
		request.Namespace,
		sas,
		role.ARN,
		awskit.RequestTags(request.Namespace, request.Spec.ClaimRef.Name, request.Name),
	)

	for _, e := range []struct {
		reason, action, verb string
		names                []string
	}{
		{eventReasonAssociationCreated, eventActionCreate, "Created", change.Created},
		{eventReasonAssociationUpdated, eventActionUpdate, "Moved", change.Updated},
		{eventReasonAssociationDeleted, eventActionDelete, "Deleted", change.Deleted},
	} {
		for _, sa := range e.names {
			req.Event(
				corev1.EventTypeNormal,
				e.reason,
				e.action,
				"%s the Pod Identity association of the ServiceAccount %s to the IAM role %s",
				e.verb,
				sa,
				role.Name,
			)
		}
	}

	return associations, err
}

// outputs returns the outputs of a request served by role.
func (p *Plugin) outputs(role awskit.Role) (map[string]string, error) {
	annotations := map[string]string{}
	if p.mode == awskit.ModeIRSA {
		annotations[annotationRoleARN] = role.ARN
	}

	raw, err := json.Marshal(annotations)
	if err != nil {
		return nil, fmt.Errorf("encoding the ServiceAccount annotations: %w", err)
	}

	return map[string]string{outputPrincipal: role.ARN, outputAnnotations: string(raw)}, nil
}

// Delete deletes the associations of the role of the request, its inline
// policies, and the role. A role that is gone is not an error.
func (p *Plugin) Delete(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	roleName := p.roleName(req.Object)
	if err := p.deleteAssociations(ctx, req, roleName); err != nil {
		return err
	}

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

// Orphan keeps the role of the request and its inline policies. With Pod
// Identity it deletes the associations, because they name the namespace of
// the request.
func (p *Plugin) Orphan(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	return p.deleteAssociations(ctx, req, p.roleName(req.Object))
}

// deleteAssociations deletes the associations of the role roleName in the
// namespace of req, with Pod Identity. It also deletes them when the role is
// gone, if req has a principal output.
func (p *Plugin) deleteAssociations(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	roleName string,
) error {
	if p.mode != awskit.ModePodIdentity {
		return nil
	}

	arn, err := p.liveRoleARN(ctx, roleName)
	if err != nil {
		return err
	}
	if arn == "" {
		// A role that someone deleted by hand can still have associations.
		arn = req.Object.Status.Outputs[outputPrincipal]
	}
	if arn == "" {
		return nil
	}

	deleted, err := awskit.DeleteAssociations(ctx, p.clients.EKS, p.eksCluster, req.Object.Namespace, arn)
	for _, sa := range deleted {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonAssociationDeleted,
			eventActionDelete,
			"Deleted the Pod Identity association of the ServiceAccount %s",
			sa,
		)
	}
	return err
}

// health checks that the AWS credentials work and that the permissions
// boundary exists.
func (p *Plugin) health(ctx context.Context) error {
	if _, err := p.clients.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
		return fmt.Errorf("checking the AWS credentials: %w", err)
	}

	if _, err := p.clients.IAM.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(p.boundary)}); err != nil {
		return fmt.Errorf("reading the permissions boundary %s: %w", p.boundary, err)
	}

	return nil
}
