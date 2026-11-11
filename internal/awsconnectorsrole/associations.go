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

	corev1 "k8s.io/api/core/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// ensureAssociations keeps, with Pod Identity, one association of roleARN
// for each of sas, deletes each other association of the request, records an
// event for each change, and returns the resources of the associations and
// the changes, which it also returns with an error. With IRSA it does
// nothing.
func (p *Plugin) ensureAssociations(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	sas []string,
	roleARN string,
) ([]pluginsv1alpha1.ResourceRef, awskit.AssociationChange, error) {
	if p.mode != awskit.ModePodIdentity {
		return nil, awskit.AssociationChange{}, nil
	}

	request := req.Object
	associations, change, err := awskit.EnsureAssociations(
		ctx,
		p.clients.EKS,
		p.eksCluster,
		request.Namespace,
		sas,
		roleARN,
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
				roleARN,
			)
		}
	}
	p.associationsDeleted(req, change.Deleted)
	if err != nil {
		return nil, change, err
	}

	resources := make([]pluginsv1alpha1.ResourceRef, 0, len(associations))
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
	return resources, change, nil
}

// deleteAssociations deletes, with Pod Identity, each association of the
// request on any role, and returns their ServiceAccounts, also with an error.
// With IRSA it does nothing.
func (p *Plugin) deleteAssociations(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) ([]string, error) {
	if p.mode != awskit.ModePodIdentity {
		return nil, nil
	}

	request := req.Object
	deleted, err := awskit.DeleteRequestAssociations(
		ctx,
		p.clients.EKS,
		p.eksCluster,
		request.Namespace,
		p.tags(request),
		nil,
	)
	p.associationsDeleted(req, deleted)
	return deleted, err
}

// associationsDeleted records an event for each ServiceAccount in sas whose
// association was deleted.
func (p *Plugin) associationsDeleted(req pluginkit.Request[Params, map[string]string], sas []string) {
	for _, sa := range sas {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonAssociationDeleted,
			eventActionDelete,
			"Deleted the Pod Identity association of the ServiceAccount %s",
			sa,
		)
	}
}
