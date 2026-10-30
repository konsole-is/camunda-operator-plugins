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
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// The registrations that the plugin serves.
const (
	// RegistrationRole gives the Connectors of each cluster an IAM role that
	// the plugin owns.
	RegistrationRole = "aws-connectors-role"
	// RegistrationExistingRole gives the Connectors of each cluster one IAM
	// role that the admin made.
	RegistrationExistingRole = "aws-connectors-existing-role"
)

// ReasonMissingServiceAccount means that a request names no
// spec.target.serviceAccountName, so no ServiceAccount gets the role.
const ReasonMissingServiceAccount = "MissingServiceAccount"

const (
	// name is the controller name of the plugin.
	name = "aws-connectors-role"
	// inlinePolicyName is the inline policy of a role of RegistrationRole.
	// The plugin owns no other inline policy.
	inlinePolicyName = "connectors"
	// awsIdentityRolePrefix starts the role names of aws-identity.
	awsIdentityRolePrefix = "camunda"
)

// The events that the plugin records on a request.
const (
	eventReasonRoleCreated         = "RoleCreated"
	eventReasonRoleUpdated         = "RoleUpdated"
	eventReasonRoleDeleted         = "RoleDeleted"
	eventReasonInlinePolicyUpdated = "InlinePolicyUpdated"
	eventReasonInlinePolicyDeleted = "InlinePolicyDeleted"
	eventReasonPolicyAttached      = "PolicyAttached"
	eventReasonPolicyDetached      = "PolicyDetached"
	eventReasonAssociationCreated  = "AssociationCreated"
	eventReasonAssociationUpdated  = "AssociationUpdated"
	eventReasonAssociationDeleted  = "AssociationDeleted"
	eventActionCreate              = "Create"
	eventActionUpdate              = "Update"
	eventActionDelete              = "Delete"
)

// The resource kinds of status.resources.
const (
	kindIAMRole                = "IAMRole"
	kindPodIdentityAssociation = "PodIdentityAssociation"
)

// Params are the parameters of both registrations. pluginkit has rendered
// the placeholders of InlinePolicy.
type Params struct {
	RoleARN           string          `json:"roleArn,omitempty"`
	ManagedPolicyARNs []string        `json:"managedPolicyArns,omitempty"`
	ExtraPolicyARNs   []string        `json:"extraPolicyArns,omitempty"`
	InlinePolicy      json.RawMessage `json:"inlinePolicy,omitempty"`
}

// Plugin serves Connectors permissions requests. Each request is the
// Connectors of one cluster, and only the ServiceAccount of spec.target gets
// its role.
type Plugin struct {
	mode       awskit.Mode
	provider   awskit.OIDCProvider
	eksCluster string
	region     string
	prefix     string
	boundary   string
	extraTags  awskit.ExtraTags
	clients    awskit.Clients
}

// Reconcile keeps the role of a RegistrationRole request, or the
// association of the role of a RegistrationExistingRole request, and
// publishes the outputs of the role.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	switch r := req.Object.Spec.Registration; r {
	case RegistrationRole:
		return p.reconcileRole(ctx, req)
	case RegistrationExistingRole:
		return p.reconcileExisting(ctx, req)
	default:
		return pluginkit.Result{}, fmt.Errorf("the plugin does not serve the registration %q", r)
	}
}

// Delete deletes the associations of the request, and the role of a
// RegistrationRole request with its policies. It never deletes the role of a
// RegistrationExistingRole request. An object that is gone is not an error.
func (p *Plugin) Delete(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	switch r := req.Object.Spec.Registration; r {
	case RegistrationRole:
		return p.deleteRole(ctx, req)
	case RegistrationExistingRole:
		return p.deleteExistingAssociations(ctx, req)
	default:
		return fmt.Errorf("the plugin does not serve the registration %q", r)
	}
}

// Orphan keeps the role and its policies. With Pod Identity it deletes the
// associations of the request, because they name its namespace.
func (p *Plugin) Orphan(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	switch r := req.Object.Spec.Registration; r {
	case RegistrationRole:
		arn, err := p.ownedRoleARN(ctx, req)
		if err != nil {
			return err
		}
		return p.deleteAssociations(ctx, req, arn)
	case RegistrationExistingRole:
		return p.deleteExistingAssociations(ctx, req)
	default:
		return fmt.Errorf("the plugin does not serve the registration %q", r)
	}
}

// tags returns the tags of each AWS object of request.
func (p *Plugin) tags(request *pluginsv1alpha1.PluginRequest) map[string]string {
	return p.extraTags.RequestTags(request.Namespace, request.Spec.ClaimRef.Name, request.Name)
}

// health checks that the AWS credentials work and that the permissions
// boundary exists, when the plugin has one.
func (p *Plugin) health(ctx context.Context) error {
	if _, err := p.clients.STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
		return fmt.Errorf("checking the AWS credentials: %w", err)
	}

	if p.boundary == "" {
		return nil
	}
	if _, err := p.clients.IAM.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(p.boundary)}); err != nil {
		return fmt.Errorf("reading the permissions boundary %s: %w", p.boundary, err)
	}

	return nil
}
