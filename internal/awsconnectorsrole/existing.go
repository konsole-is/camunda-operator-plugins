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

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// reconcileExisting publishes the outputs of the role that the admin named
// in roleArn. It never calls IAM. With Pod Identity it keeps the association
// of the Connectors ServiceAccount to the role.
func (p *Plugin) reconcileExisting(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	sas := trustedServiceAccounts(req.Object.Spec.Target)
	if len(sas) == 0 {
		return pluginkit.Result{Pending: missingServiceAccount()}, nil
	}

	roleARN := req.Parameters.RoleARN
	consoleURL, err := awskit.RoleConsoleURL(roleARN)
	if err != nil {
		return pluginkit.Result{Pending: &pluginkit.Waiting{
			Reason:  pluginsv1alpha1.ReasonInvalidParameters,
			Message: "spec.parameters.roleArn: " + err.Error(),
		}}, nil
	}

	resources, err := p.ensureAssociations(ctx, req, sas, roleARN)
	if err != nil {
		return pluginkit.Result{}, err
	}

	return pluginkit.Result{
		Outputs: awskit.IdentityOutputs(p.mode, roleARN),
		Summary: []pluginsv1alpha1.SummaryItem{
			{Label: "Role", Value: roleARN},
			{Label: "Managed by", Value: "The admin"},
		},
		Links:     []pluginsv1alpha1.Link{{Name: "IAM role", URL: consoleURL}},
		Resources: resources,
	}, nil
}

// existingRoleARN is the role of a RegistrationExistingRole request: roleArn,
// or the principal output when the parameters name none.
func existingRoleARN(req pluginkit.Request[Params, map[string]string]) string {
	if req.Parameters.RoleARN != "" {
		return req.Parameters.RoleARN
	}
	return req.Object.Status.Outputs[awskit.OutputPrincipal]
}
