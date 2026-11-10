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

package awsebs

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// The events that the plugin records on a request.
const (
	eventReasonStorageClassCreated  = "StorageClassCreated"
	eventReasonStorageClassReplaced = "StorageClassReplaced"
	eventReasonStorageClassDeleted  = "StorageClassDeleted"
	eventActionCreate               = "Create"
	eventActionReplace              = "Replace"
	eventActionDelete               = "Delete"
)

// kindStorageClass is the kind of the row of status.resources.
const kindStorageClass = "StorageClass"

// Plugin serves volume encryption requests. Each request gets one
// StorageClass of the EBS CSI driver that encrypts with the key of the
// request. The plugin makes no AWS call.
type Plugin struct {
	client client.Client
	reader client.Reader
}

// Reconcile returns the StorageClass of the request and publishes its name.
// It is Pending with the reason InvalidKey when the input key is not the ARN
// of a KMS key, and with StorageClassNameTaken when a StorageClass of its name
// is not of the request. When a parameter that a StorageClass cannot change
// differs, it deletes the StorageClass and creates it again under the same
// name. Volumes that exist keep their parameters.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	key := req.Inputs[InputKey]
	console, err := awskit.KeyConsoleURL(key)
	if err != nil {
		return pluginkit.Result{Pending: &pluginkit.Waiting{
			Reason:  ReasonInvalidKey,
			Message: fmt.Sprintf("the input key is not the ARN of a KMS key: %v", err),
		}}, nil
	}

	name := StorageClassName(req.Object.Namespace, req.Object.Spec.ClaimRef.Name, req.Object.Name)
	desired := StorageClass(name, req.Parameters, key)

	// The cache can still hold a StorageClass that the last pass deleted.
	var live storagev1.StorageClass
	err = p.reader.Get(ctx, client.ObjectKey{Name: name}, &live)
	notFound := apierrors.IsNotFound(err)
	replaced := false
	switch {
	case notFound:
	case err != nil:
		return pluginkit.Result{}, fmt.Errorf("reading the StorageClass %s: %w", name, err)
	case !ofRequest(&live, req.Object):
		return pluginkit.Result{Pending: &pluginkit.Waiting{
			Reason: ReasonStorageClassNameTaken,
			Message: fmt.Sprintf(
				"the StorageClass %s exists and is not of this request: delete it, and the plugin creates it again",
				name,
			),
		}}, nil
	case !live.DeletionTimestamp.IsZero():
		return pluginkit.Result{}, fmt.Errorf("waiting for the old StorageClass %s to be gone", name)
	default:
		if replaced, err = p.deleteChanged(ctx, req, &live, desired); err != nil {
			return pluginkit.Result{}, err
		}
	}

	// The forced apply of pluginkit would take over a StorageClass that
	// another actor created after the read, so a missing one is created first.
	if notFound || replaced {
		if err := p.create(ctx, req.Object, desired); err != nil {
			return pluginkit.Result{}, err
		}
	}
	if notFound {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonStorageClassCreated,
			eventActionCreate,
			"Created the StorageClass %s",
			name,
		)
	}

	return pluginkit.Result{
		Outputs: map[string]string{OutputStorageClassName: name},
		Objects: []client.Object{desired},
		Summary: []pluginsv1alpha1.SummaryItem{
			{Label: "StorageClass", Value: name},
			{Label: "Volume type", Value: desired.Parameters[paramType]},
			{Label: "Key", Value: key},
		},
		Links: []pluginsv1alpha1.Link{{Name: "KMS key", URL: console}},
		Resources: []pluginsv1alpha1.ResourceRef{{
			Kind:     kindStorageClass,
			Name:     name,
			Role:     "Volumes of the cluster",
			OnOrphan: pluginsv1alpha1.OnOrphanKept,
		}},
	}, nil
}

// ofRequest reports whether sc carries each of the ClusterScopedLabels of
// request.
func ofRequest(sc *storagev1.StorageClass, request *pluginsv1alpha1.PluginRequest) bool {
	for k, v := range pluginkit.ClusterScopedLabels(request) {
		if sc.Labels[k] != v {
			return false
		}
	}
	return true
}

// deleteChanged deletes live when a field that a StorageClass cannot change
// differs from desired, records one event that names the fields, and reports
// whether it deleted live.
func (p *Plugin) deleteChanged(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
	live, desired *storagev1.StorageClass,
) (bool, error) {
	changed := ChangedImmutable(live, desired)
	if len(changed) == 0 {
		return false, nil
	}

	uid := live.UID
	if err := p.client.Delete(ctx, live, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
		return false, fmt.Errorf("deleting the StorageClass %s to replace it: %w", live.Name, err)
	}
	req.Event(
		corev1.EventTypeNormal,
		eventReasonStorageClassReplaced,
		eventActionReplace,
		"Replaced the StorageClass %s, because %s changed. Volumes that exist keep their parameters",
		live.Name,
		strings.Join(changed, ", "),
	)
	return true, nil
}

// create creates sc with the ClusterScopedLabels of request. It returns an
// error when a StorageClass of its name exists, so that the next pass reads
// that StorageClass.
func (p *Plugin) create(ctx context.Context, request *pluginsv1alpha1.PluginRequest, sc *storagev1.StorageClass) error {
	obj := sc.DeepCopy()
	obj.Labels = pluginkit.ClusterScopedLabels(request)
	if err := p.client.Create(ctx, obj); err != nil {
		return fmt.Errorf("creating the StorageClass %s: %w", sc.Name, err)
	}
	return nil
}

// Delete deletes the StorageClass of the request. Volumes that exist stay.
// It returns an error while the StorageClass still exists.
func (p *Plugin) Delete(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	deleted, err := pluginkit.DeleteClusterScoped(ctx, p.reader, p.client, req.Object, &storagev1.StorageClassList{})
	for _, obj := range deleted {
		req.Event(
			corev1.EventTypeNormal,
			eventReasonStorageClassDeleted,
			eventActionDelete,
			"Deleted the StorageClass %s. Volumes that exist stay",
			obj.GetName(),
		)
	}
	return err
}

// Orphan keeps the StorageClass of the request with its labels. It has no
// owner reference to remove.
func (p *Plugin) Orphan(context.Context, pluginkit.Request[Params, map[string]string]) error {
	return nil
}
