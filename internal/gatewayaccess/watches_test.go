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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

func targetRequest(name, kind, target string) *pluginsv1alpha1.PluginRequest {
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: name},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Target: pluginsv1alpha1.TargetRef{Kind: kind, Name: target},
		},
	}
}

// requestsOfChange returns the requests that the watch of the kind of obj
// reconciles for a change of obj.
func requestsOfChange(t *testing.T, obj client.Object) []reconcile.Request {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, pluginsv1alpha1.AddToScheme(scheme))
	require.NoError(t, v1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		targetRequest("orders-access", "CamundaCluster", "orders"),
		targetRequest("plane-access", "CamundaManagementCluster", "orders"),
		targetRequest("other-access", "CamundaCluster", "other"),
	).Build()

	for _, w := range watches(c) {
		if reflect.TypeOf(w.Object) == reflect.TypeOf(obj) {
			return w.Requests(context.Background(), obj)
		}
	}
	require.FailNowf(t, "no watch", "no watch for %T", obj)
	return nil
}

func request(name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: name}}
}

func TestWatches_ClusterMapsToTheRequestsThatTargetIt(t *testing.T) {
	cluster := &v1.CamundaCluster{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "orders"}}
	assert.Equal(t, []reconcile.Request{request("orders-access")}, requestsOfChange(t, cluster))
}

func TestWatches_OptimizeMapsToTheRequestsOfItsCluster(t *testing.T) {
	optimize := &v1.CamundaOptimize{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "analytics"},
		Spec:       v1.CamundaOptimizeSpec{ClusterRef: v1.ClusterRef{Name: "orders"}},
	}
	assert.Equal(t, []reconcile.Request{request("orders-access")}, requestsOfChange(t, optimize))
}

func TestWatches_PlaneMapsToTheRequestsThatTargetIt(t *testing.T) {
	plane := &v1.CamundaManagementCluster{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "orders"}}
	assert.Equal(t, []reconcile.Request{request("plane-access")}, requestsOfChange(t, plane))
}

func TestWatches_TargetInAnotherNamespaceMapsToNoRequest(t *testing.T) {
	cluster := &v1.CamundaCluster{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", Name: "orders"}}
	assert.Empty(t, requestsOfChange(t, cluster))
}
