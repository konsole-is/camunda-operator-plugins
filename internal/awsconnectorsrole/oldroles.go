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
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

const (
	// oldRoleRequeue is the wait before the pass that deletes an old role,
	// after pluginkit wrote the outputs that name the new one.
	oldRoleRequeue = 5 * time.Second
	// roleInUseRequeue is the wait before a request of RoleNameInUse checks
	// the role of its name again. A change of that role in AWS sends no
	// watch event.
	roleInUseRequeue = 30 * time.Second
)

// retireOldRoles deletes the roles that the IAMRole rows of the status of
// req name besides role, once the outputs of that status name role. It
// returns the rows of the old roles that stay for a later pass. A role of
// another request UID stays in IAM, and its row goes.
func (p *Plugin) retireOldRoles(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	role awskit.Role,
) ([]pluginsv1alpha1.ResourceRef, error) {
	status := req.Object.Status
	old := oldRoleRows(status.Resources, role.Name)
	// Pods and other plugins read the role from the outputs, so the old role
	// stays until pluginkit wrote the outputs of the new one.
	if len(old) == 0 || status.Outputs[awskit.OutputPrincipal] != role.ARN {
		return old, nil
	}

	for _, row := range old {
		if err := p.deleteNamedRole(ctx, req, row.Name, role.Name); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// oldRoleRows returns the IAMRole rows of rows whose names
// awskit.ReplacedRoles keeps for current, each once. An empty current keeps
// every IAMRole row.
func oldRoleRows(rows []pluginsv1alpha1.ResourceRef, current string) []pluginsv1alpha1.ResourceRef {
	var names []string
	for _, r := range rows {
		if r.Kind == kindIAMRole {
			names = append(names, r.Name)
		}
	}

	replaced := awskit.ReplacedRoles(current, names)
	var old []pluginsv1alpha1.ResourceRef
	for _, r := range rows {
		if i := slices.Index(replaced, r.Name); r.Kind == kindIAMRole && i >= 0 {
			old = append(old, r)
			replaced = slices.Delete(replaced, i, i+1)
		}
	}
	return old
}

// activeOldRole returns the old role of the status of req that its outputs
// name, when the request owns it. It returns a zero Role when the outputs
// name no old role, or when that role is gone or belongs to another request.
func (p *Plugin) activeOldRole(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	current string,
) (awskit.Role, error) {
	status := req.Object.Status
	for _, row := range oldRoleRows(status.Resources, current) {
		if row.ID == "" || row.ID != status.Outputs[awskit.OutputPrincipal] {
			continue
		}

		role, err := awskit.OwnedRole(ctx, p.clients.IAM, row.Name, p.roleTags(req.Object))
		if errors.As(err, new(awskit.RoleInUseError)) {
			return awskit.Role{}, nil
		}
		return role, err
	}
	return awskit.Role{}, nil
}

// deleteNamedRole deletes the role name of req and records an event. A role
// of another request UID stays, with a Warning event. successor, when not
// empty, is the role that replaces it.
func (p *Plugin) deleteNamedRole(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	name, successor string,
) error {
	// OwnedRole tags a role that an older version of the plugin made, so that
	// DeleteRole finds the UID of the request on it.
	_, err := awskit.OwnedRole(ctx, p.clients.IAM, name, p.roleTags(req.Object))
	if err != nil && !errors.As(err, new(awskit.RoleInUseError)) {
		return err
	}

	deleted, err := awskit.DeleteRole(ctx, p.clients.IAM, name, string(req.Object.UID))
	var inUse awskit.RoleInUseError
	switch {
	case errors.As(err, &inUse):
		req.Event(
			corev1.EventTypeWarning,
			eventReasonRoleKept,
			eventActionDelete,
			"Kept the IAM role %s: it belongs to %s",
			name,
			inUse.Owner,
		)
	case err != nil:
		return err
	case deleted && successor != "":
		req.Event(
			corev1.EventTypeNormal,
			eventReasonRoleDeleted,
			eventActionDelete,
			"Deleted the IAM role %s. The IAM role %s replaces it",
			name,
			successor,
		)
	case deleted:
		req.Event(corev1.EventTypeNormal, eventReasonRoleDeleted, eventActionDelete, "Deleted the IAM role %s", name)
	}
	return nil
}
