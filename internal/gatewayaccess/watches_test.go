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
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

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

// gatewayRequest is a request in team-a whose parameters name the Gateway
// gateway-system/<gateway>.
func gatewayRequest(t *testing.T, name, gateway string) *pluginsv1alpha1.PluginRequest {
	t.Helper()
	p := validParams()
	p.GatewayRef.Name = gateway
	raw, err := json.Marshal(p)
	require.NoError(t, err)

	r := targetRequest(name, "CamundaCluster", "orders")
	r.Spec.Parameters = &apiextensionsv1.JSON{Raw: raw}
	return r
}

func classGateway(name, class string) *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gateway-system", Name: name},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: gatewayv1.ObjectName(class)},
	}
}

// requestsOfGatewayChange returns the requests that the watch of the kind of
// obj reconciles for a change of obj, with requests on the Gateways public
// and internal, of the classes example and other.
func requestsOfGatewayChange(t *testing.T, obj client.Object) []reconcile.Request {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, pluginsv1alpha1.AddToScheme(scheme))
	require.NoError(t, gatewayv1.Install(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		gatewayRequest(t, "public-access", "public"),
		gatewayRequest(t, "internal-access", "internal"),
		classGateway("public", "example"),
		classGateway("internal", "other"),
	).Build()

	for _, w := range watches(c) {
		if reflect.TypeOf(w.Object) == reflect.TypeOf(obj) {
			return w.Requests(context.Background(), obj)
		}
	}
	require.FailNowf(t, "no watch", "no watch for %T", obj)
	return nil
}

func TestWatches_GatewayMapsToTheRequestsThatNameIt(t *testing.T) {
	assert.Equal(t, []reconcile.Request{request("public-access")},
		requestsOfGatewayChange(t, classGateway("public", "example")))
}

func TestWatches_GatewayClassMapsToTheRequestsOfItsGateways(t *testing.T) {
	class := &gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "other"}}
	assert.Equal(t, []reconcile.Request{request("internal-access")}, requestsOfGatewayChange(t, class))
}

func TestWatches_GatewayWatchesSkipStatusChanges(t *testing.T) {
	for _, w := range watches(nil) {
		switch w.Object.(type) {
		case *gatewayv1.Gateway, *gatewayv1.GatewayClass:
			assert.Contains(
				t,
				w.Predicates,
				predicate.Predicate(predicate.GenerationChangedPredicate{}),
				"%T",
				w.Object,
			)
		}
	}
}
