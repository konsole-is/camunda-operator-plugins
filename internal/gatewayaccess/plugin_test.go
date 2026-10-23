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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
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
