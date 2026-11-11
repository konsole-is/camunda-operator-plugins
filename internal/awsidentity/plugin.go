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
// its associations, puts the allowed Grants of the cluster on the role, and
// writes their conditions. It is Pending with the reason
// MissingServiceAccount when spec.target names no ServiceAccount, and with
// the reason RoleNameInUse when the role of its name belongs to another
// request. After a change of the role name, it deletes the old role once the
// outputs name the new one, and until then keeps the old role in
// status.resources. A failed AWS call is an error, except a policy document
// that IAM refuses, which sets Applied=False on its Grant.
//
// The result names in Corrected the drift that it repaired, also with an
// error.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	request := req.Object
	sas := serviceAccounts(request.Spec.Target)
	if len(sas) == 0 {
		return p.reconcileWithoutServiceAccounts(ctx, req)
	}

	current := p.roleName(request)
	spec, hash, err := p.desiredRole(request, current, sas)
	if err != nil {
		return pluginkit.Result{}, err
	}

	role, change, err := p.ensureRole(ctx, req, spec, hash)
	pending, inUse := roleNameInUse(err, request.Status.Resources)
	if inUse {
		// The outputs still name the old role, so it serves the request until
		// the name is free.
		old, oldErr := p.activeOldRole(ctx, req, current)
		if oldErr != nil || old.ARN == "" {
			return pending, oldErr
		}
		spec.Name = old.Name
		role, change, err = p.ensureRole(ctx, req, spec, hash)
	}
	drift := role.Desired == hash
	var corrected []string
	if drift {
		corrected = change.Corrections(role.Name)
	}
	if err != nil {
		return pluginkit.Result{Corrected: corrected}, err
	}

	// A ServiceAccount that left the request loses its association before a
	// Grant step can fail.
	var leftErr error
	if p.mode == awskit.ModePodIdentity {
		var left []string
		left, leftErr = p.deleteLeftAssociations(ctx, req, sas)
		if drift {
			corrected = append(corrected, awskit.AssociationChange{Deleted: left}.Corrections()...)
		}
	}

	// The Grants reach the role before an association moves to it, so a pod
	// never holds the role without its policies.
	sync, err := p.syncGrants(ctx, req, role)
	if drift {
		corrected = append(corrected, awskit.InlinePolicyCorrections(role.Name, sync.repaired...)...)
	}
	if err := errors.Join(leftErr, err); err != nil {
		// The ServiceAccounts that the old role trusts keep their sessions, so
		// a Grant that is gone loses its policy there also when a step fails.
		return pluginkit.Result{Corrected: corrected}, errors.Join(err, p.syncOldRoleGrants(ctx, req, role, current))
	}

	resources := []pluginsv1alpha1.ResourceRef{roleResource(role, request)}
	if p.mode == awskit.ModePodIdentity {
		associations, moved, err := p.ensureAssociations(ctx, req, sas, role)
		if drift {
			corrected = append(corrected, moved.Corrections()...)
		}
		if err != nil {
			return pluginkit.Result{Corrected: corrected}, errors.Join(
				err,
				p.syncOldRoleGrants(ctx, req, role, current),
			)
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

	if _, err := awskit.MarkDesired(ctx, p.clients.IAM, role, spec.Tags, hash); err != nil {
		return pluginkit.Result{Corrected: corrected}, err
	}

	if inUse {
		pending.Resources = append(resources, oldRoleRows(request.Status.Resources, role.Name)...)
		pending.Corrected = corrected
		if sync.conflicted {
			pending.RequeueAfter = conflictRequeue
		}
		return pending, nil
	}

	old, _, err := p.retireOldRoles(ctx, req, role)
	if err != nil {
		return pluginkit.Result{Corrected: corrected}, err
	}
	resources = append(resources, old...)

	consoleURL, err := awskit.RoleConsoleURL(role.ARN)
	if err != nil {
		return pluginkit.Result{Corrected: corrected}, err
	}

	res := pluginkit.Result{
		Outputs:   awskit.IdentityOutputs(p.mode, role.ARN),
		Summary:   summary(role, p.boundary, sync.counts.applied, sync.counts.total, sync.counts.size),
		Links:     []pluginsv1alpha1.Link{{Name: "IAM role", URL: consoleURL}},
		Resources: resources,
		Corrected: corrected,
	}
	if len(old) > 0 {
		res.RequeueAfter = oldRoleRequeue
	}
	if sync.conflicted {
		res.RequeueAfter = conflictRequeue
	}

	return res, nil
}

// roleNameInUse returns the Pending result of a request whose role name
// belongs to another request, when err is an awskit.RoleInUseError. The
// result keeps the old roles of the rows of status.
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

// syncOldRoleGrants keeps the Grants of the old role that the outputs of req
// name in sync, when it is not role.
func (p *Plugin) syncOldRoleGrants(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	role awskit.Role,
	current string,
) error {
	old, err := p.activeOldRole(ctx, req, current)
	if err != nil || old.ARN == "" || old.Name == role.Name {
		return err
	}

	_, err = p.syncGrants(ctx, req, old)
	return err
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
// and the old role that the outputs name trust nobody, and keeps their Grants
// in sync, because credentials that STS gave before stay valid. It deletes the
// other old roles of the status only once the outputs name the role of its
// current name. The result names the drift that it repaired, also with an
// error.
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

	arn, err := p.liveRoleARN(ctx, current)
	if err != nil {
		return res, err
	}

	// The pass of the old role deletes the associations of the request, also
	// those of the role of the current name, so that role gets its desired
	// tag first.
	var role awskit.Role
	var conflicted, inUse bool
	var pending pluginkit.Result
	if arn != "" {
		role, conflicted, res.Corrected, err = p.trustNobody(ctx, req, current)
		pending, inUse = roleNameInUse(err, status)
		if err != nil && !inUse {
			return pluginkit.Result{Corrected: res.Corrected}, err
		}
	}

	old, err := p.activeOldRole(ctx, req, current)
	if err != nil {
		return pluginkit.Result{Corrected: res.Corrected}, err
	}
	if old.ARN != "" {
		_, oldConflicted, corrected, err := p.trustNobody(ctx, req, old.Name)
		res.Corrected = append(res.Corrected, corrected...)
		if err != nil {
			return pluginkit.Result{Corrected: res.Corrected}, err
		}
		if oldConflicted {
			res.RequeueAfter = conflictRequeue
		}
	}

	switch {
	case inUse:
		pending.Corrected = res.Corrected
		return pending, nil
	case arn == "" && old.ARN == "":
		// The associations of a role that someone deleted by hand still hold
		// the ServiceAccounts.
		return res, p.deleteAssociations(ctx, req, current)
	case arn == "":
		return res, nil
	}

	left, again, err := p.retireOldRoles(ctx, req, role)
	if err != nil {
		return pluginkit.Result{Corrected: res.Corrected}, err
	}

	res.Resources = append([]pluginsv1alpha1.ResourceRef{roleResource(role, req.Object)}, left...)
	if again {
		res.RequeueAfter = oldRoleRequeue
	}
	if conflicted {
		res.RequeueAfter = conflictRequeue
	}
	return res, nil
}

// trustNobody makes the role name of req trust nobody, deletes the
// associations of req, keeps the Grants of the role in sync, and then tags the role with the
// hash of that state. It returns the drift that it repaired, also with an
// error, and whether a Grant changed after it was read.
func (p *Plugin) trustNobody(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	name string,
) (awskit.Role, bool, []string, error) {
	spec, hash, err := p.desiredRole(req.Object, name, nil)
	if err != nil {
		return awskit.Role{}, false, nil, err
	}

	role, change, err := p.ensureRole(ctx, req, spec, hash)
	drift := role.Desired == hash
	var corrected []string
	if drift {
		corrected = change.Corrections(role.Name)
	}
	if err != nil {
		return awskit.Role{}, false, corrected, err
	}

	if p.mode == awskit.ModePodIdentity {
		_, deleted, err := p.ensureAssociations(ctx, req, nil, role)
		if drift {
			corrected = append(corrected, deleted.Corrections()...)
		}
		if err != nil {
			sync, syncErr := p.syncGrants(ctx, req, role)
			if drift {
				corrected = append(corrected, awskit.InlinePolicyCorrections(role.Name, sync.repaired...)...)
			}
			return awskit.Role{}, false, corrected, errors.Join(err, syncErr)
		}
	}

	sync, err := p.syncGrants(ctx, req, role)
	if drift {
		corrected = append(corrected, awskit.InlinePolicyCorrections(role.Name, sync.repaired...)...)
	}
	if err != nil {
		return awskit.Role{}, false, corrected, err
	}

	if _, err := awskit.MarkDesired(ctx, p.clients.IAM, role, spec.Tags, hash); err != nil {
		return awskit.Role{}, false, corrected, err
	}
	return role, sync.conflicted, corrected, nil
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

// desiredRole returns the role name of the claim of request, trusted by sas,
// or by nobody when sas is empty, and the hash of that desired state.
func (p *Plugin) desiredRole(
	request *pluginsv1alpha1.PluginRequest,
	name string,
	sas []string,
) (awskit.RoleSpec, string, error) {
	trust, err := awskit.TrustPolicy(p.mode, p.provider, request.Namespace, sas)
	if err != nil {
		return awskit.RoleSpec{}, "", err
	}

	spec := awskit.RoleSpec{
		Name:                name,
		Description:         "Camunda cluster " + request.Namespace + "/" + request.Spec.ClaimRef.Name,
		TrustPolicy:         trust,
		PermissionsBoundary: p.boundary,
		Tags:                p.roleTags(request),
	}
	// With Pod Identity the trust policy does not name the ServiceAccounts,
	// and the associations follow them.
	return spec, spec.DesiredHash([]byte(strings.Join(sas, ","))), nil
}

// ensureRole calls awskit.EnsureRole with spec and hash, records an event
// when it created or updated the role, and returns the role and the change,
// also with an error.
func (p *Plugin) ensureRole(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	spec awskit.RoleSpec,
	hash string,
) (awskit.Role, awskit.RoleChange, error) {
	role, change, err := awskit.EnsureRole(ctx, p.clients.IAM, spec, hash)

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

	return role, change, err
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

// deleteLeftAssociations deletes the associations of req of each
// ServiceAccount that is not in sas, records an event for each, and returns
// their ServiceAccounts, also with an error.
func (p *Plugin) deleteLeftAssociations(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	sas []string,
) ([]string, error) {
	request := req.Object
	deleted, err := awskit.DeleteRequestAssociations(
		ctx,
		p.clients.EKS,
		p.eksCluster,
		request.Namespace,
		p.tags(request),
		sas,
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
	return deleted, err
}

// ensureAssociations keeps one Pod Identity association of role for each of
// sas, records an event for each change, and returns the changes, also with
// an error.
func (p *Plugin) ensureAssociations(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	sas []string,
	role awskit.Role,
) ([]awskit.Association, awskit.AssociationChange, error) {
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

	return associations, change, err
}

// Delete deletes the associations of the current role of the request and of
// each role of its status.resources. It then deletes each of those roles with
// its inline policies, only when the role has the UID of the request. A role
// of another request UID stays, with a Warning event. A role that is gone is
// not an error.
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
