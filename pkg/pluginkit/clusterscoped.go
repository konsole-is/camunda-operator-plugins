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

package pluginkit

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// ClusterScopedLabels returns the labels that pluginkit gives each
// cluster-scoped object of request: registration, request, and
// request-namespace. An object is of request only when it carries all three.
func ClusterScopedLabels(request *pluginsv1alpha1.PluginRequest) map[string]string {
	return map[string]string{
		pluginsv1alpha1.LabelRegistration:     request.Spec.Registration,
		pluginsv1alpha1.LabelRequest:          request.Name,
		pluginsv1alpha1.LabelRequestNamespace: request.Namespace,
	}
}

// DeleteClusterScoped deletes each object of the kind of list that carries
// the ClusterScopedLabels of request, and returns the objects that it deleted,
// also with an error. It lists with reader, so a list from the API server
// finds an object that the cache does not hold yet. It deletes with writer and
// the UID of the listed object, and skips an object that is already deleting.
// It returns an error that names each object of request that still exists
// after the deletes, for example one that a finalizer holds, so that Delete
// returns it and pluginkit keeps the request finalizer. An object that is
// already gone is not an error.
func DeleteClusterScoped(
	ctx context.Context,
	reader client.Reader,
	writer client.Writer,
	request *pluginsv1alpha1.PluginRequest,
	list client.ObjectList,
) ([]client.Object, error) {
	objects, err := listClusterScoped(ctx, reader, request, list)
	if err != nil {
		return nil, err
	}

	var deleted []client.Object
	for _, obj := range objects {
		if !obj.GetDeletionTimestamp().IsZero() {
			continue
		}

		uid := obj.GetUID()
		if err := writer.Delete(ctx, obj, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
			return deleted, fmt.Errorf("deleting %q: %w", obj.GetName(), err)
		}
		deleted = append(deleted, obj)
	}

	// A read into list would overwrite the deleted objects, which point
	// into its items.
	remaining, err := listClusterScoped(ctx, reader, request, list.DeepCopyObject().(client.ObjectList))
	if err != nil {
		return deleted, err
	}
	if len(remaining) > 0 {
		names := make([]string, 0, len(remaining))
		for _, obj := range remaining {
			names = append(names, obj.GetName())
		}
		return deleted, fmt.Errorf("waiting for the objects of the request to be gone: %s", strings.Join(names, ", "))
	}

	return deleted, nil
}

// listClusterScoped lists the objects of the kind of list that carry the
// ClusterScopedLabels of request.
func listClusterScoped(
	ctx context.Context,
	reader client.Reader,
	request *pluginsv1alpha1.PluginRequest,
	list client.ObjectList,
) ([]client.Object, error) {
	if err := reader.List(ctx, list, client.MatchingLabels(ClusterScopedLabels(request))); err != nil {
		return nil, fmt.Errorf("listing the cluster-scoped objects of the request: %w", err)
	}

	items, err := meta.ExtractList(list)
	if err != nil {
		return nil, fmt.Errorf("reading the cluster-scoped objects of the request: %w", err)
	}

	objects := make([]client.Object, 0, len(items))
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok {
			return nil, fmt.Errorf("the list holds %T, which is not an object", item)
		}
		objects = append(objects, obj)
	}
	return objects, nil
}
