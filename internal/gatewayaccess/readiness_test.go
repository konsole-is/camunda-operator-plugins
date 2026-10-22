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

// liveCopy returns a copy of route in team-a as the API server stores it,
// with the generation and one status.parents entry for parent with conds.
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
	setLiveBackendDefaults(obj)

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

func TestRouteWaiting_TerminatingRouteIsPending(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)[:1]
	terminating := liveCopy(desired[0], 1, parent, accepted(1))
	terminating.SetDeletionTimestamp(&metav1.Time{Time: testNow})
	terminating.SetFinalizers([]string{"example.com/hold"})

	n, waiting := RouteWaiting(desired, liveByKey(terminating), parent)
	assert.Equal(t, 0, n)
	require.NotNil(t, waiting)
	assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)
}

func TestRouteWaiting_RouteThatAnotherWriterChangedIsPending(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)[:1]
	drifted := liveCopy(desired[0], 2, parent, accepted(2))
	route, ok := drifted.(*gatewayv1.HTTPRoute)
	require.True(t, ok)
	route.Spec.Rules[0].BackendRefs[0].Name = "other-service"

	n, waiting := RouteWaiting(desired, liveByKey(drifted), parent)
	assert.Equal(t, 0, n)
	require.NotNil(t, waiting)
	assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)
}

// setLiveBackendDefaults writes the group, the kind, and the weight that the
// API server defaults on each backend of route.
func setLiveBackendDefaults(route client.Object) {
	switch r := route.(type) {
	case *gatewayv1.HTTPRoute:
		for i := range r.Spec.Rules {
			for j := range r.Spec.Rules[i].BackendRefs {
				setBackendDefaults(&r.Spec.Rules[i].BackendRefs[j].BackendRef)
			}
		}
	case *gatewayv1.GRPCRoute:
		for i := range r.Spec.Rules {
			for j := range r.Spec.Rules[i].BackendRefs {
				setBackendDefaults(&r.Spec.Rules[i].BackendRefs[j].BackendRef)
			}
		}
	}
}

func TestRouteWaiting_RouteWithAMatchConstraintOfAnotherWriterIsPending(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)[:1]

	for name, constrain := range map[string]func(*gatewayv1.HTTPRouteMatch){
		"method": func(m *gatewayv1.HTTPRouteMatch) { m.Method = new(gatewayv1.HTTPMethodGet) },
		"header": func(m *gatewayv1.HTTPRouteMatch) {
			m.Headers = []gatewayv1.HTTPHeaderMatch{{Name: "X-Team", Value: "a"}}
		},
		"query": func(m *gatewayv1.HTTPRouteMatch) {
			m.QueryParams = []gatewayv1.HTTPQueryParamMatch{{Name: "debug", Value: "1"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			live := liveCopy(desired[0], 2, parent, accepted(2))
			route, ok := live.(*gatewayv1.HTTPRoute)
			require.True(t, ok)
			constrain(&route.Spec.Rules[0].Matches[0])

			n, waiting := RouteWaiting(desired, liveByKey(live), parent)
			assert.Equal(t, 0, n)
			require.NotNil(t, waiting)
			assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)
		})
	}
}

func TestRouteWaiting_RouteWithAFilterOfAnotherWriterIsPending(t *testing.T) {
	p := validParams()
	parent := ParentRef(p)
	desired := clusterRoutes(t, p)
	header := gatewayv1.HTTPRouteFilter{Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier}
	grpcHeader := gatewayv1.GRPCRouteFilter{Type: gatewayv1.GRPCRouteFilterRequestHeaderModifier}

	for name, change := range map[string]func(client.Object){
		"HTTP rule filter": func(obj client.Object) {
			if r, ok := obj.(*gatewayv1.HTTPRoute); ok {
				r.Spec.Rules[0].Filters = []gatewayv1.HTTPRouteFilter{header}
			}
		},
		"HTTP backend filter": func(obj client.Object) {
			if r, ok := obj.(*gatewayv1.HTTPRoute); ok {
				r.Spec.Rules[0].BackendRefs[0].Filters = []gatewayv1.HTTPRouteFilter{header}
			}
		},
		"gRPC rule filter": func(obj client.Object) {
			if r, ok := obj.(*gatewayv1.GRPCRoute); ok {
				r.Spec.Rules[0].Filters = []gatewayv1.GRPCRouteFilter{grpcHeader}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			live := make([]client.Object, 0, len(desired))
			for _, obj := range desired {
				copied := liveCopy(obj, 2, parent, accepted(2))
				change(copied)
				live = append(live, copied)
			}

			n, waiting := RouteWaiting(desired, liveByKey(live...), parent)
			assert.Equal(t, 1, n)
			require.NotNil(t, waiting)
			assert.Equal(t, string(gatewayv1.RouteReasonPending), waiting.Reason)
		})
	}
}
