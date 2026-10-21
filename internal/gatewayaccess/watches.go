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

package gatewayaccess

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// watches reconciles a request again when its target changes, or when a
// CamundaOptimize of its cluster changes.
func watches(c client.Reader) []pluginkit.Watch {
	return []pluginkit.Watch{
		{
			Object: &v1.CamundaCluster{},
			Requests: func(ctx context.Context, obj client.Object) []reconcile.Request {
				return requestsFor(ctx, c, obj.GetNamespace(), kindCamundaCluster, obj.GetName())
			},
		},
		{
			Object: &v1.CamundaManagementCluster{},
			Requests: func(ctx context.Context, obj client.Object) []reconcile.Request {
				return requestsFor(ctx, c, obj.GetNamespace(), kindCamundaManagementCluster, obj.GetName())
			},
		},
		{
			Object: &v1.CamundaOptimize{},
			Requests: func(ctx context.Context, obj client.Object) []reconcile.Request {
				optimize, ok := obj.(*v1.CamundaOptimize)
				if !ok {
					return nil
				}
				return requestsFor(ctx, c, obj.GetNamespace(), kindCamundaCluster, optimize.Spec.ClusterRef.Name)
			},
		},
	}
}

// requestsFor returns the requests of namespace whose target has kind and
// name. A failed list returns none.
func requestsFor(ctx context.Context, c client.Reader, namespace, kind, name string) []reconcile.Request {
	var list pluginsv1alpha1.PluginRequestList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, r := range list.Items {
		if r.Spec.Target.Kind == kind && r.Spec.Target.Name == name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: r.Name},
			})
		}
	}
	return requests
}
