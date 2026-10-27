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
	"maps"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// kindCamundaCluster is the target kind of the requests that Grants name.
const kindCamundaCluster = "CamundaCluster"

// watches reconciles the requests of a cluster again when a Grant of the
// cluster changes its spec or its labels, comes, or goes, when another request of the
// cluster changes its spec or its outputs, and when the spec of a
// registration changes that a Grant of the cluster names. pluginkit ignores
// the mapped requests of registrations that the hub does not serve.
func watches(c client.Reader) []pluginkit.Watch {
	return []pluginkit.Watch{
		{
			Object: &pluginsv1alpha1.Grant{},
			Requests: func(ctx context.Context, obj client.Object) []reconcile.Request {
				grant, ok := obj.(*pluginsv1alpha1.Grant)
				if !ok {
					return nil
				}
				return clusterRequests(ctx, c, grant.Namespace, grant.Spec.Cluster.Name)
			},
			// The labels name the request and the registration of a Grant.
			Predicates: []predicate.Predicate{
				predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}),
			},
		},
		{
			Object: &pluginsv1alpha1.PluginRequest{},
			Requests: func(ctx context.Context, obj client.Object) []reconcile.Request {
				request, ok := obj.(*pluginsv1alpha1.PluginRequest)
				if !ok || request.Spec.Target.Kind != kindCamundaCluster {
					return nil
				}
				return clusterRequests(ctx, c, request.Namespace, request.Spec.Target.Name)
			},
			Predicates: []predicate.Predicate{specOrOutputsChanged()},
		},
		{
			Object: &pluginsv1alpha1.PluginRegistration{},
			Requests: func(ctx context.Context, obj client.Object) []reconcile.Request {
				return registrationRequests(ctx, c, obj.GetName())
			},
			Predicates: []predicate.Predicate{predicate.GenerationChangedPredicate{}},
		},
	}
}

// specOrOutputsChanged passes an update of a PluginRequest that changes its
// spec or its outputs, and every create and delete.
func specOrOutputsChanged() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		before, ok := e.ObjectOld.(*pluginsv1alpha1.PluginRequest)
		after, ok2 := e.ObjectNew.(*pluginsv1alpha1.PluginRequest)
		if !ok || !ok2 {
			return false
		}
		return before.Generation != after.Generation || !maps.Equal(before.Status.Outputs, after.Status.Outputs)
	}}
}

// clusterRequests returns the requests of namespace whose target is the
// CamundaCluster cluster. A failed list returns none.
func clusterRequests(ctx context.Context, c client.Reader, namespace, cluster string) []reconcile.Request {
	if cluster == "" {
		return nil
	}

	var list pluginsv1alpha1.PluginRequestList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, r := range list.Items {
		if r.Spec.Target.Kind == kindCamundaCluster && r.Spec.Target.Name == cluster {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: r.Name},
			})
		}
	}
	return requests
}

// registrationRequests returns the requests of each cluster that has a
// Grant of the registration. A failed list returns none.
func registrationRequests(ctx context.Context, c client.Reader, registration string) []reconcile.Request {
	var grants pluginsv1alpha1.GrantList
	if err := c.List(ctx, &grants, client.MatchingLabels{pluginsv1alpha1.LabelRegistration: registration}); err != nil {
		return nil
	}

	clusters := map[types.NamespacedName]bool{}
	for _, g := range grants.Items {
		clusters[types.NamespacedName{Namespace: g.Namespace, Name: g.Spec.Cluster.Name}] = true
	}

	var requests []reconcile.Request
	for cluster := range clusters {
		requests = append(requests, clusterRequests(ctx, c, cluster.Namespace, cluster.Name)...)
	}
	return requests
}
