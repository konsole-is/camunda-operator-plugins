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
	"errors"
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

// The reasons of OutputsReady and Ready of a request that waits.
const (
	// ReasonMissingServiceAccount means that a request names no
	// ServiceAccount in spec.target, so its role would trust nobody.
	ReasonMissingServiceAccount = "MissingServiceAccount"
	// ReasonRoleNameInUse means that the IAM role of the name of a request
	// belongs to another request, or to none. The plugin does not change it.
	ReasonRoleNameInUse = "RoleNameInUse"
)

// The events that the hub records on a request.
const (
	eventReasonRoleCreated        = "RoleCreated"
	eventReasonRoleUpdated        = "RoleUpdated"
	eventReasonRoleDeleted        = "RoleDeleted"
	eventReasonRoleKept           = "RoleKept"
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
	prefix     string
	extraTags  awskit.ExtraTags
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
// MissingServiceAccount when spec.target names no ServiceAccount, and with
// the reason RoleNameInUse when the role of its name belongs to another
// request. After a change of the role name, it deletes the old role once the
// outputs name the new one, and until then keeps the old role in
// status.resources. A failed AWS call is an error, except a policy document
// that IAM refuses, which sets Applied=False on its Grant.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	request := req.Object
	sas := serviceAccounts(request.Spec.Target)
	if len(sas) == 0 {
		return p.reconcileWithoutServiceAccounts(ctx, req)
	}

	role, err := p.ensureRole(ctx, req, p.roleName(request), sas)
	if res, ok, err := p.roleNameInUse(ctx, req, err); ok {
		return res, err
	}
	if err != nil {
		return pluginkit.Result{}, err
	}

	resources := []pluginsv1alpha1.ResourceRef{roleResource(role, request)}
	if p.mode == awskit.ModePodIdentity {
		associations, err := p.ensureAssociations(ctx, req, sas, role)
		if err != nil {
			// The ServiceAccounts that the role trusts keep their sessions, so
			// a Grant that is gone loses its policy also when an association
			// fails.
			_, _, syncErr := p.syncGrants(ctx, req, role)
			return pluginkit.Result{}, errors.Join(err, syncErr)
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

	old, err := p.retireOldRoles(ctx, req, role)
	if err != nil {
		return pluginkit.Result{}, err
	}
	resources = append(resources, old...)

	consoleURL, err := awskit.RoleConsoleURL(role.ARN)
	if err != nil {
		return pluginkit.Result{}, err
	}

	res := pluginkit.Result{
		Outputs:   awskit.IdentityOutputs(p.mode, role.ARN),
		Summary:   summary(role, p.boundary, counts.applied, counts.total, counts.size),
		Links:     []pluginsv1alpha1.Link{{Name: "IAM role", URL: consoleURL}},
		Resources: resources,
	}
	if len(old) > 0 {
		res.RequeueAfter = oldRoleRequeue
	}
	if conflicted {
		res.RequeueAfter = conflictRequeue
	}

	return res, nil
}

// roleNameInUse returns the Pending result of req when err is an
// awskit.RoleInUseError for the role of its name. The result keeps the old
// roles of the status. The outputs still name the old role, so it keeps the
// Grants of that role in sync, and a deleted Grant loses its policy.
func (p *Plugin) roleNameInUse(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	err error,
) (pluginkit.Result, bool, error) {
	var inUse awskit.RoleInUseError
	if !errors.As(err, &inUse) {
		return pluginkit.Result{}, false, nil
	}

	res := pluginkit.Result{
		Pending:      &pluginkit.Waiting{Reason: ReasonRoleNameInUse, Message: inUse.Error()},
		Resources:    oldRoleRows(req.Object.Status.Resources, inUse.Name),
		RequeueAfter: roleInUseRequeue,
	}

	old, err := p.activeOldRole(ctx, req, inUse.Name)
	if err != nil || old.ARN == "" {
		return res, true, err
	}

	_, conflicted, err := p.syncGrants(ctx, req, old)
	if conflicted {
		res.RequeueAfter = conflictRequeue
	}
	return res, true, err
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
// gave before stay valid. Before the role of its current name exists, it does
// this to the old role that the outputs name. It keeps the old roles of the
// status and deletes none, because the outputs can still name one.
func (p *Plugin) reconcileWithoutServiceAccounts(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	current := p.roleName(req.Object)
	status := req.Object.Status.Resources
	res := pluginkit.Result{
		Pending: &pluginkit.Waiting{
			Reason:  ReasonMissingServiceAccount,
			Message: "The request names no ServiceAccount in spec.target",
		},
		Resources: oldRoleRows(status, current),
	}

	name := current
	arn, err := p.liveRoleARN(ctx, current)
	if err != nil {
		return res, err
	}
	if arn == "" {
		old, err := p.activeOldRole(ctx, req, current)
		if err != nil {
			return res, err
		}
		if old.ARN == "" {
			// The associations of a role that someone deleted by hand still
			// hold the ServiceAccounts.
			return res, p.deleteAssociations(ctx, req, current)
		}
		name = old.Name
	}

	role, err := p.ensureRole(ctx, req, name, nil)
	if inUse, ok, err := p.roleNameInUse(ctx, req, err); ok {
		return inUse, err
	}
	if err != nil {
		return pluginkit.Result{}, err
	}

	if p.mode == awskit.ModePodIdentity {
		if _, err := p.ensureAssociations(ctx, req, nil, role); err != nil {
			_, _, syncErr := p.syncGrants(ctx, req, role)
			return pluginkit.Result{}, errors.Join(err, syncErr)
		}
	}

	_, conflicted, err := p.syncGrants(ctx, req, role)
	if err != nil {
		return pluginkit.Result{}, err
	}

	res.Resources = append(
		[]pluginsv1alpha1.ResourceRef{roleResource(role, req.Object)},
		oldRoleRows(status, role.Name)...)
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

// ensureRole keeps the role name of the claim of req, trusted by sas, or by
// nobody when sas is empty, and records an event when it created or updated
// the role.
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
		Description:         "Camunda cluster " + request.Namespace + "/" + request.Spec.ClaimRef.Name,
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
// EKS cluster, and the EKS cluster name names one in its region.
func (p *Plugin) roleName(request *pluginsv1alpha1.PluginRequest) string {
	scope := p.provider.Issuer
	if p.mode == awskit.ModePodIdentity {
		scope = p.eksCluster
	}
	return awskit.RoleName(p.prefix, scope, request.Namespace, request.Spec.ClaimRef.Name)
}

// tags returns the tags of each AWS object of request.
func (p *Plugin) tags(request *pluginsv1alpha1.PluginRequest) map[string]string {
	return p.extraTags.RequestTags(request.Namespace, request.Spec.ClaimRef.Name, request.Name)
}

// roleTags returns the tags of the role of request, which bind it to the UID
// of request.
func (p *Plugin) roleTags(request *pluginsv1alpha1.PluginRequest) map[string]string {
	return p.extraTags.OwnerTags(request.Namespace, request.Spec.ClaimRef.Name, request.Name, string(request.UID))
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
		p.tags(request),
	)

	for _, e := range []struct {
		reason, action, verb string
		names                []string
	}{
		{eventReasonAssociationCreated, eventActionCreate, "Created", change.Created},
		{eventReasonAssociationUpdated, eventActionUpdate, "Moved", change.Updated},
		{eventReasonAssociationUpdated, eventActionUpdate, "Tagged", change.Tagged},
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
	for _, sa := range change.Deleted {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonAssociationDeleted,
			eventActionDelete,
			"Deleted the Pod Identity association of the ServiceAccount %s",
			sa,
		)
	}

	return associations, err
}

// Delete deletes each role of the request: the role of its current name and
// the roles of its status.resources, each only with the UID of the request,
// with their associations and inline policies. A role of another request UID
// stays, with a Warning event. A role that is gone is not an error.
func (p *Plugin) Delete(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	current := p.roleName(req.Object)
	old := oldRoleRows(req.Object.Status.Resources, current)
	names := make([]string, 0, len(old)+1)
	names = append(names, current)
	for _, row := range old {
		names = append(names, row.Name)
	}

	for _, name := range names {
		if err := p.deleteAssociations(ctx, req, name); err != nil {
			return err
		}

		if err := p.deleteNamedRole(ctx, req, name, ""); err != nil {
			return err
		}
	}
	return nil
}

// Orphan keeps the role of the request and its inline policies. With Pod
// Identity it deletes the associations, because they name the namespace of
// the request.
func (p *Plugin) Orphan(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	return p.deleteAssociations(ctx, req, p.roleName(req.Object))
}

// deleteAssociations deletes the associations of the role roleName that
// carry the tags of req, with Pod Identity. It also deletes them when the role is
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
		arn = req.Object.Status.Outputs[awskit.OutputPrincipal]
	}
	if arn == "" {
		return nil
	}

	request := req.Object
	deleted, err := awskit.DeleteAssociations(
		ctx,
		p.clients.EKS,
		p.eksCluster,
		request.Namespace,
		arn,
		p.tags(request),
	)
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
