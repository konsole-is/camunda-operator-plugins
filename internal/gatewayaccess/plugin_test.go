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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func TestStaleRoutes_ReturnsRoutesThatAreNotWanted(t *testing.T) {
	http := func(name string) client.Object {
		return &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	grpc := func(name string) client.Object {
		return &gatewayv1.GRPCRoute{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	live := []client.Object{http("a"), grpc("a-grpc"), http("a-optimize")}
	desired := []client.Object{http("a"), grpc("a-grpc")}
	assert.Equal(t, []client.Object{http("a-optimize")}, StaleRoutes(live, desired))

	// A GRPCRoute and an HTTPRoute with one name are two routes.
	assert.Equal(t, []client.Object{grpc("a")}, StaleRoutes([]client.Object{grpc("a")}, []client.Object{http("a")}))
	assert.Empty(t, StaleRoutes(live, live))
}

func TestDeleteRoutes_DeletesOnlyTheListedRoute(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, gatewayv1.Install(scheme))
	var preconditions *metav1.Preconditions
	// The fake client does not check preconditions, so the interceptor
	// answers as the API server does for a route that replaced the listed one.
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			var o client.DeleteOptions
			o.ApplyOptions(opts)
			preconditions = o.Preconditions
			return apierrors.NewConflict(schema.GroupResource{Resource: "httproutes"}, obj.GetName(), nil)
		},
	}).Build()
	listed := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "orders-access", UID: "listed"},
	}

	err := deleteRoutes(context.Background(), c, pluginkit.Request[Params, map[string]string]{},
		[]client.Object{listed}, "whose endpoint is gone")
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err), err)
	require.NotNil(t, preconditions)
	assert.Equal(t, listed.UID, *preconditions.UID)
}
