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
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

// The events that pluginkit records on a request.
const (
	eventReasonGrantDeleted      = "GrantDeleted"
	eventReasonResourcesOrphaned = "ResourcesOrphaned"
	eventActionDelete            = "Delete"
	eventActionOrphan            = "Orphan"
)

// delete applies the deletion policy of a deleted request, and then releases
// its finalizer. The finalizer stays while the plugin fails, and while a Grant
// of the request still exists.
func (r *reconciler[P, I]) delete(
	ctx context.Context,
	request *pluginsv1alpha1.PluginRequest,
	registration *pluginsv1alpha1.PluginRegistration,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(request, pluginsv1alpha1.FinalizerCleanup) {
		return ctrl.Result{}, nil
	}

	// A deleted request is not checked against the schema, so that a changed
	// schema does not block its delete.
	parameters := request.Spec.Parameters
	if data, err := templateData(request); err == nil {
		if rendered, err := renderParameters(registration.Spec.Parameters, parameters, data); err == nil {
			parameters = rendered
		}
	}

	pluginRequest, err := r.pluginRequest(request, registration, parameters, log)
	if err != nil {
		stage(request, v1.ConditionReady, pluginsv1alpha1.ReasonInvalidParameters, err.Error())
		return ctrl.Result{}, err
	}

	policy := request.Spec.DeletionPolicy
	if policy == pluginsv1alpha1.DeletionPolicyOrphan {
		err = r.plugin.Orphan(ctx, pluginRequest)
	} else {
		err = r.plugin.Delete(ctx, pluginRequest)
	}
	if err != nil {
		stage(request, v1.ConditionReady, ReasonDeleteFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("applying the deletion policy %s: %w", policy, err)
	}

	if policy == pluginsv1alpha1.DeletionPolicyOrphan {
		err = r.orphan(ctx, request)
	} else {
		var remaining int
		remaining, err = r.deleteGrants(ctx, request)
		if err == nil && remaining > 0 {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
	}
	if err != nil {
		stage(request, v1.ConditionReady, ReasonDeleteFailed, err.Error())
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(request, pluginsv1alpha1.FinalizerCleanup)
	if err := r.client.Update(ctx, request); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing the finalizer: %w", err)
	}
	log.Info("Released the cleanup finalizer of PluginRequest", "deletionPolicy", policy)

	return ctrl.Result{}, nil
}

// deleteGrants deletes each Grant with the request label of request, also a
// Grant without an owner reference, and returns how many still exist.
func (r *reconciler[P, I]) deleteGrants(ctx context.Context, request *pluginsv1alpha1.PluginRequest) (int, error) {
	grants, err := r.grantsOf(ctx, request)
	if err != nil {
		return 0, err
	}

	for _, g := range grants {
		if g.DeletionTimestamp.IsZero() {
			err := r.client.Delete(ctx, &g, client.Preconditions{UID: &g.UID})
			if client.IgnoreNotFound(err) != nil {
				return 0, fmt.Errorf("deleting Grant %q: %w", g.Name, err)
			}
			if err == nil {
				r.events.Eventf(request, &g, corev1.EventTypeNormal, eventReasonGrantDeleted, eventActionDelete,
					"Deleted Grant %s", g.Name)
			}
		}
	}

	// A Grant can hold a finalizer of the identity extension, so the list
	// is read again.
	grants, err = r.grantsOf(ctx, request)
	return len(grants), err
}

// orphan removes the owner references to request from its Grants and from the
// objects of the namespaced kinds of Config.Owns, also from an object that lost
// its labels. They keep their labels.
func (r *reconciler[P, I]) orphan(ctx context.Context, request *pluginsv1alpha1.PluginRequest) error {
	kinds := append([]client.Object{&pluginsv1alpha1.Grant{}}, r.namespacedOwns...)

	count := 0
	for _, kind := range kinds {
		gvk, err := apiutil.GVKForObject(kind, r.scheme)
		if err != nil {
			return fmt.Errorf("finding the kind of %T: %w", kind, err)
		}

		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := r.reader.List(ctx, list, client.InNamespace(request.Namespace)); err != nil {
			return fmt.Errorf("listing the %s objects of the request: %w", gvk.Kind, err)
		}

		for i := range list.Items {
			if !slices.ContainsFunc(list.Items[i].GetOwnerReferences(), func(ref metav1.OwnerReference) bool {
				return ref.UID == request.UID
			}) {
				continue
			}

			released, err := r.release(ctx, &list.Items[i], request)
			if err != nil {
				return err
			}
			if released {
				count++
			}
		}
	}

	r.events.Eventf(request, nil, corev1.EventTypeNormal, eventReasonResourcesOrphaned, eventActionOrphan,
		"Removed the owner reference to the request from %d objects", count)

	return nil
}

// release removes the owner references to request from obj, and reports
// whether obj had one.
func (r *reconciler[P, I]) release(
	ctx context.Context,
	obj *unstructured.Unstructured,
	request *pluginsv1alpha1.PluginRequest,
) (bool, error) {
	released := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.reader.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}

		refs := obj.GetOwnerReferences()
		kept := refs[:0]
		for _, ref := range refs {
			if ref.UID != request.UID {
				kept = append(kept, ref)
			}
		}
		if len(kept) == len(refs) {
			return nil
		}

		obj.SetOwnerReferences(kept)
		released = true
		return r.client.Update(ctx, obj)
	})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("removing the owner reference of %s %q: %w", obj.GetKind(), obj.GetName(), err)
	}

	return released, nil
}
