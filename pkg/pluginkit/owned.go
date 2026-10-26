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
	"fmt"
	"maps"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

// prepareOwned returns a copy of obj, ready to apply for req: without its
// status, and with the registration and request labels added to its own. A
// namespaced object goes to the namespace of req when it names none, and gets
// a controller owner reference to req. A cluster-scoped object gets no owner
// reference, and also gets the request namespace label. A namespaced object in
// another namespace, and a cluster-scoped object with a namespace, are errors.
func prepareOwned(
	req *pluginsv1alpha1.PluginRequest,
	obj client.Object,
	namespaced bool,
	scheme *runtime.Scheme,
) (*unstructured.Unstructured, error) {
	gvk, err := apiutil.GVKForObject(obj, scheme)
	if err != nil {
		return nil, fmt.Errorf("finding the kind of %T %q: %w", obj, obj.GetName(), err)
	}

	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj.DeepCopyObject())
	if err != nil {
		return nil, fmt.Errorf("converting %s %q: %w", gvk.Kind, obj.GetName(), err)
	}

	u := &unstructured.Unstructured{Object: content}
	u.SetGroupVersionKind(gvk)
	delete(u.Object, "status")

	labels := maps.Clone(u.GetLabels())
	if labels == nil {
		labels = map[string]string{}
	}
	labels[pluginsv1alpha1.LabelRegistration] = req.Spec.Registration
	labels[pluginsv1alpha1.LabelRequest] = req.Name
	u.SetLabels(labels)

	if !namespaced {
		if u.GetNamespace() != "" {
			return nil, fmt.Errorf(
				"%s %q is cluster-scoped and cannot have the namespace %q",
				gvk.Kind,
				u.GetName(),
				u.GetNamespace(),
			)
		}
		labels[pluginsv1alpha1.LabelRequestNamespace] = req.Namespace
		u.SetLabels(labels)
		return u, nil
	}

	switch u.GetNamespace() {
	case "":
		u.SetNamespace(req.Namespace)
	case req.Namespace:
	default:
		return nil, fmt.Errorf(
			"%s %q is in the namespace %q: an object of a request must be in the namespace %q of the request",
			gvk.Kind, u.GetName(), u.GetNamespace(), req.Namespace,
		)
	}

	if err := controllerutil.SetControllerReference(req, u, scheme); err != nil {
		return nil, fmt.Errorf("setting the owner of %s %q: %w", gvk.Kind, u.GetName(), err)
	}

	return u, nil
}
