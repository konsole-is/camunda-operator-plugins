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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

func ownedScheme(t *testing.T) *runtime.Scheme {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, pluginsv1alpha1.AddToScheme(scheme))
	return scheme
}

func ownedRequest() *pluginsv1alpha1.PluginRequest {
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-backup-storage", Namespace: "team-a", UID: "uid-1"},
		Spec:       pluginsv1alpha1.PluginRequestSpec{Registration: "s3-bucket"},
	}
}

func wantLabels(extra map[string]string) map[string]string {
	labels := map[string]string{
		pluginsv1alpha1.LabelRegistration: "s3-bucket",
		pluginsv1alpha1.LabelRequest:      "orders-backup-storage",
	}
	for k, v := range extra {
		labels[k] = v
	}
	return labels
}

func TestPrepareOwnedGivesANamespacedObjectAnOwnerReference(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "data"}}

	got, err := prepareOwned(ownedRequest(), cm, true, ownedScheme(t))

	require.NoError(t, err)
	assert.Equal(t, "team-a", got.GetNamespace())
	assert.Equal(t, wantLabels(nil), got.GetLabels())
	assert.Equal(t, []metav1.OwnerReference{{
		APIVersion:         pluginsv1alpha1.GroupVersion.String(),
		Kind:               "PluginRequest",
		Name:               "orders-backup-storage",
		UID:                "uid-1",
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}}, got.GetOwnerReferences())
	assert.Equal(t, "ConfigMap", got.GetKind())
	assert.Empty(t, cm.Labels, "the object of the plugin stays as it was")
}

func TestPrepareOwnedGivesAClusterScopedObjectTheLabelsOnly(t *testing.T) {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "camunda-team-a-orders"}, Provisioner: "x"}

	got, err := prepareOwned(ownedRequest(), sc, false, ownedScheme(t))

	require.NoError(t, err)
	assert.Equal(t, wantLabels(nil), got.GetLabels())
	assert.Empty(t, got.GetOwnerReferences())
	assert.Empty(t, got.GetNamespace())
}

func TestPrepareOwnedKeepsTheLabelsOfThePlugin(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "data", Labels: map[string]string{"app": "x"}}}

	got, err := prepareOwned(ownedRequest(), cm, true, ownedScheme(t))

	require.NoError(t, err)
	assert.Equal(t, wantLabels(map[string]string{"app": "x"}), got.GetLabels())
}

func TestPrepareOwnedKeepsTheNamespaceOfTheRequest(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "team-a"}}

	got, err := prepareOwned(ownedRequest(), cm, true, ownedScheme(t))

	require.NoError(t, err)
	assert.Equal(t, "team-a", got.GetNamespace())
}

func TestPrepareOwnedRefusesAnotherNamespace(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "other"}}

	_, err := prepareOwned(ownedRequest(), cm, true, ownedScheme(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ConfigMap")
	assert.Contains(t, err.Error(), `"data"`)
}

func TestPrepareOwnedRefusesAClusterScopedObjectWithANamespace(t *testing.T) {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast", Namespace: "team-a"}}

	_, err := prepareOwned(ownedRequest(), sc, false, ownedScheme(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "StorageClass")
	assert.Contains(t, err.Error(), `"fast"`)
}

func TestPrepareOwnedDropsTheStatus(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}

	got, err := prepareOwned(ownedRequest(), pvc, true, ownedScheme(t))

	require.NoError(t, err)
	assert.NotContains(t, got.Object, "status")
}
