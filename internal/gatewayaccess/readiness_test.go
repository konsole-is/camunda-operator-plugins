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
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// condition is one route condition, as the Gateway writes it.
type condition struct {
	conditionType gatewayv1.RouteConditionType
	status        metav1.ConditionStatus
	reason        string
	message       string
	generation    int64
}

func accepted(generation int64) []condition {
	return []condition{
		{gatewayv1.RouteConditionAccepted, metav1.ConditionTrue, "Accepted", "", generation},
		{gatewayv1.RouteConditionResolvedRefs, metav1.ConditionTrue, "ResolvedRefs", "", generation},
	}
}

// liveCopy returns a copy of route in team-a with the generation and one
// status.parents entry for parent with conds.
func liveCopy(
	route client.Object,
	generation int64,
	parent gatewayv1.ParentReference,
	conds []condition,
) client.Object {
	obj, ok := route.DeepCopyObject().(client.Object)
	if !ok {
		panic("route is not a client.Object")
	}
	obj.SetNamespace("team-a")
	obj.SetGeneration(generation)

	entry := gatewayv1.RouteParentStatus{ParentRef: parent, ControllerName: "example.com/gateway"}
	for _, c := range conds {
		entry.Conditions = append(entry.Conditions, metav1.Condition{
			Type:               string(c.conditionType),
			Status:             c.status,
			Reason:             c.reason,
			Message:            c.message,
			ObservedGeneration: c.generation,
		})
	}

	switch r := obj.(type) {
	case *gatewayv1.HTTPRoute:
		r.Status.Parents = append(r.Status.Parents, entry)
	case *gatewayv1.GRPCRoute:
		r.Status.Parents = append(r.Status.Parents, entry)
	}
	return obj
}

func liveByKey(objs ...client.Object) map[RouteKey]client.Object {
	live := map[RouteKey]client.Object{}
	for _, obj := range objs {
		live[KeyOf(obj)] = obj
	}
	return live
}

func clusterRoutes(t *testing.T, p Params) []client.Object {
	t.Helper()
	routes := ClusterRoutes("orders-access", p, testHosts(t), standaloneEndpoints())
	require.Len(t, routes, 2)
	return routes
}

func TestRouteWaiting_AllAcceptedIsNotWaiting(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)
	live := liveByKey(liveCopy(desired[0], 1, parent, accepted(1)), liveCopy(desired[1], 4, parent, accepted(4)))

	n, waiting := RouteWaiting(desired, live, parent)
	assert.Equal(t, 2, n)
	assert.Nil(t, waiting)
}

func TestRouteWaiting_MissingRouteIsPending(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)
	live := liveByKey(liveCopy(desired[1], 1, parent, accepted(1)))

	n, waiting := RouteWaiting(desired, live, parent)
	assert.Equal(t, 1, n)
	require.NotNil(t, waiting)
	assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)
	assert.Contains(t, waiting.Message, "HTTPRoute orders-access waits for the Gateway gateway-system/public")
}

func TestRouteWaiting_RefusedRouteGivesTheReasonOfTheRoute(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)
	refused := []condition{
		{
			gatewayv1.RouteConditionAccepted,
			metav1.ConditionFalse,
			"NotAllowedByListeners",
			"no listener admits the namespace",
			1,
		},
		{gatewayv1.RouteConditionResolvedRefs, metav1.ConditionTrue, "ResolvedRefs", "", 1},
	}
	live := liveByKey(liveCopy(desired[0], 1, parent, refused), liveCopy(desired[1], 1, parent, accepted(1)))

	n, waiting := RouteWaiting(desired, live, parent)
	assert.Equal(t, 1, n)
	require.NotNil(t, waiting)
	assert.Equal(t, "NotAllowedByListeners", waiting.Reason)
	assert.Equal(t, "HTTPRoute orders-access: no listener admits the namespace", waiting.Message)
}

func TestRouteWaiting_UnresolvedBackendGivesTheReason(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)
	unresolved := []condition{
		{gatewayv1.RouteConditionAccepted, metav1.ConditionTrue, "Accepted", "", 1},
		{
			gatewayv1.RouteConditionResolvedRefs,
			metav1.ConditionFalse,
			"BackendNotFound",
			"no Service orders-operate",
			1,
		},
	}
	live := liveByKey(liveCopy(desired[0], 1, parent, unresolved), liveCopy(desired[1], 1, parent, accepted(1)))

	_, waiting := RouteWaiting(desired, live, parent)
	require.NotNil(t, waiting)
	assert.Equal(t, "BackendNotFound", waiting.Reason)
	assert.Equal(t, "HTTPRoute orders-access: no Service orders-operate", waiting.Message)
}

func TestRouteWaiting_StatusOfAnOlderGenerationIsPending(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)[:1]
	live := liveByKey(liveCopy(desired[0], 3, parent, accepted(2)))

	n, waiting := RouteWaiting(desired, live, parent)
	assert.Equal(t, 0, n)
	require.NotNil(t, waiting)
	assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)
}

func TestRouteWaiting_IgnoresTheStatusOfOtherParents(t *testing.T) {
	p := validParams()
	desired := clusterRoutes(t, p)[:1]
	other := p
	other.GatewayRef.Name = "internal"
	live := liveByKey(liveCopy(desired[0], 1, ParentRef(other), accepted(1)))

	n, waiting := RouteWaiting(desired, live, ParentRef(p))
	assert.Equal(t, 0, n)
	require.NotNil(t, waiting)
	assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)

	withListener := p
	withListener.ListenerName = httpsListener
	desired = clusterRoutes(t, withListener)[:1]
	live = liveByKey(liveCopy(desired[0], 1, ParentRef(p), accepted(1)))

	n, waiting = RouteWaiting(desired, live, ParentRef(withListener))
	assert.Equal(t, 0, n)
	require.NotNil(t, waiting)
	assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)
}

func TestRouteWaiting_NamesTheFirstRouteByKindThenName(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)
	refused := []condition{{gatewayv1.RouteConditionAccepted, metav1.ConditionFalse, "NotAllowedByListeners", "no", 1}}
	live := liveByKey(liveCopy(desired[0], 1, parent, refused), liveCopy(desired[1], 1, parent, refused))

	_, waiting := RouteWaiting(desired, live, parent)
	require.NotNil(t, waiting)
	assert.Equal(t, "GRPCRoute orders-access-grpc: no", waiting.Message)
}

func TestRouteWaiting_ChangedRouteIsPendingUntilItsNewSpecIsApplied(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)[:1]
	before := ClusterRoutes("orders-access", p, testHosts(t), embeddedEndpoints())[:1]
	live := liveByKey(liveCopy(before[0], 1, parent, accepted(1)))

	n, waiting := RouteWaiting(desired, live, parent)
	assert.Equal(t, 0, n)
	require.NotNil(t, waiting)
	assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)
}
