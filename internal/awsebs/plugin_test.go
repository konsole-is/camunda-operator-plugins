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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func TestReconcileLeavesAStorageClassThatAnotherActorCreatedAfterTheReadAlone(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	request := &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-volume-encryption", Namespace: "team-a"},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Registration: "aws-ebs-encrypted",
			ClaimRef:     pluginsv1alpha1.ClaimRef{Name: "orders"},
		},
	}
	name := StorageClassName("team-a", "orders", "orders-volume-encryption")
	foreign := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: "example.com/other"}
	// The read finds no StorageClass, and the write finds the one that
	// another actor created in between.
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	writer := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()
	plugin := &Plugin{client: writer, reader: reader}

	res, err := plugin.Reconcile(t.Context(), pluginkit.Request[Params, map[string]string]{
		Object: request,
		Inputs: map[string]string{InputKey: testKey},
	})

	require.Error(t, err)
	assert.Empty(t, res.Objects, "pluginkit must not apply over the StorageClass of another actor")
	var current storagev1.StorageClass
	require.NoError(t, writer.Get(t.Context(), client.ObjectKey{Name: name}, &current))
	assert.Equal(t, "example.com/other", current.Provisioner)
	assert.Empty(t, current.Labels)
}

func TestReconcileWaitsWithoutAnErrorForTheOldStorageClassToBeGone(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	request := &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-volume-encryption", Namespace: "team-a"},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Registration: "aws-ebs-encrypted",
			ClaimRef:     pluginsv1alpha1.ClaimRef{Name: "orders"},
		},
	}
	deleting := StorageClass(StorageClassName("team-a", "orders", "orders-volume-encryption"), Params{}, testKey)
	deleting.Labels = pluginkit.ClusterScopedLabels(request)
	deleting.Finalizers = []string{"example.com/hold"}
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deleting).Build()
	plugin := &Plugin{client: c, reader: c}

	res, err := plugin.Reconcile(t.Context(), pluginkit.Request[Params, map[string]string]{
		Object: request,
		Inputs: map[string]string{InputKey: testKey},
	})

	require.NoError(t, err)
	require.NotNil(t, res.Pending)
	assert.Equal(t, ReasonStorageClassDeleting, res.Pending.Reason)
	assert.Contains(t, res.Pending.Message, deleting.Name)
	assert.Positive(t, res.RequeueAfter)
	assert.Empty(t, res.Objects)
}
