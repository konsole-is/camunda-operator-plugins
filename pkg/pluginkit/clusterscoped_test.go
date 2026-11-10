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
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func TestClusterScopedLabels(t *testing.T) {
	request := ownedRequest()

	assert.Equal(t, map[string]string{
		pluginsv1alpha1.LabelRegistration:     request.Spec.Registration,
		pluginsv1alpha1.LabelRequest:          request.Name,
		pluginsv1alpha1.LabelRequestNamespace: request.Namespace,
	}, ClusterScopedLabels(request))
}

func TestDeleteClusterScopedDeletesOnlyTheObjectsOfTheRequest(t *testing.T) {
	request := ownedRequest()
	own := storageClassWith("own", ClusterScopedLabels(request))
	otherNamespace := storageClassWith("other-namespace", withLabel(request,
		pluginsv1alpha1.LabelRequestNamespace, "elsewhere"))
	otherRegistration := storageClassWith("other-registration", withLabel(request,
		pluginsv1alpha1.LabelRegistration, "other"))
	noNamespaceLabel := storageClassWith("no-namespace-label", withLabel(request,
		pluginsv1alpha1.LabelRequestNamespace, ""))
	c := fake.NewClientBuilder().
		WithScheme(ownedScheme(t)).
		WithObjects(own, otherNamespace, otherRegistration, noNamespaceLabel).
		Build()

	deleted, err := DeleteClusterScoped(t.Context(), c, c, request, &storagev1.StorageClassList{})

	require.NoError(t, err)
	require.Len(t, deleted, 1)
	assert.Equal(t, "own", deleted[0].GetName())
	assert.True(t, apierrors.IsNotFound(c.Get(t.Context(), client.ObjectKeyFromObject(own), &storagev1.StorageClass{})))
	for _, kept := range []*storagev1.StorageClass{otherNamespace, otherRegistration, noNamespaceLabel} {
		assert.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(kept), &storagev1.StorageClass{}), kept.Name)
	}
}

func TestDeleteClusterScopedSucceedsWhenNothingIsLeft(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(ownedScheme(t)).Build()

	deleted, err := DeleteClusterScoped(t.Context(), c, c, ownedRequest(), &storagev1.StorageClassList{})

	require.NoError(t, err)
	assert.Empty(t, deleted)
}

func TestDeleteClusterScopedWaitsForAnObjectThatAFinalizerHolds(t *testing.T) {
	request := ownedRequest()
	own := storageClassWith("own", ClusterScopedLabels(request))
	own.Finalizers = []string{"example.com/hold"}
	c := fake.NewClientBuilder().WithScheme(ownedScheme(t)).WithObjects(own).Build()

	deleted, err := DeleteClusterScoped(t.Context(), c, c, request, &storagev1.StorageClassList{})

	require.Len(t, deleted, 1)
	assert.Equal(t, "own", deleted[0].GetName())
	assert.ErrorContains(t, err, "own")

	deleted, err = DeleteClusterScoped(t.Context(), c, c, request, &storagev1.StorageClassList{})

	assert.Empty(t, deleted, "an object that is already deleting is not deleted again")
	assert.ErrorContains(t, err, "own")
}

func storageClassWith(name string, labels map[string]string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name, Labels: labels},
		Provisioner: "example.com/none",
	}
}

// withLabel returns the ClusterScopedLabels of request with key set to value,
// or without key when value is empty.
func withLabel(request *pluginsv1alpha1.PluginRequest, key, value string) map[string]string {
	labels := maps.Clone(ClusterScopedLabels(request))
	if value == "" {
		delete(labels, key)
		return labels
	}
	labels[key] = value
	return labels
}
