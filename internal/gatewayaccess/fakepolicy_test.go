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

package gatewayaccess_test

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// examplePolicyKind is the kind of testdata/crds/examplepolicies.yaml.
var examplePolicyKind = schema.GroupVersionKind{
	Group:   "policies.example.com",
	Version: "v1alpha1",
	Kind:    "ExamplePolicy",
}

// The verdicts of the fake policy controller for a policy name.
const (
	verdictRefuse = "refuse"
	verdictIgnore = "ignore"
)

// startFakePolicyController adds to mgr a controller that stands in for the
// policy controller of the fake Gateway. For each ExamplePolicy it writes one
// status.ancestors entry for the Gateway gateway-system/public with
// Accepted=True for the generation of the policy, or Accepted=False with the
// reason Invalid when verdicts holds verdictRefuse for the name of the
// policy. It writes nothing for verdictIgnore.
func startFakePolicyController(mgr ctrl.Manager, verdicts *sync.Map) error {
	c := mgr.GetClient()
	kind := &unstructured.Unstructured{}
	kind.SetGroupVersionKind(examplePolicyKind)

	return ctrl.NewControllerManagedBy(mgr).
		Named("fake-gateway-examplepolicy").
		For(kind).
		Complete(reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
			policy := &unstructured.Unstructured{}
			policy.SetGroupVersionKind(examplePolicyKind)
			if err := c.Get(ctx, req.NamespacedName, policy); err != nil {
				return reconcile.Result{}, client.IgnoreNotFound(err)
			}

			verdict, _ := verdicts.Load(policy.GetName())
			if verdict == verdictIgnore {
				return reconcile.Result{}, nil
			}
			return reconcile.Result{}, writeAncestors(ctx, c, policy, verdict == verdictRefuse)
		}))
}

// writeAncestors sets status.ancestors of policy and writes it when it
// changed.
func writeAncestors(ctx context.Context, c client.Client, policy *unstructured.Unstructured, refused bool) error {
	accepted := metav1.Condition{
		Type:               string(gatewayv1.PolicyConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.PolicyReasonAccepted),
		Message:            "The fake Gateway accepts the policy",
		ObservedGeneration: policy.GetGeneration(),
		LastTransitionTime: metav1.Now(),
	}
	if refused {
		accepted.Status = metav1.ConditionFalse
		accepted.Reason = string(gatewayv1.PolicyReasonInvalid)
		accepted.Message = "the fake Gateway refuses the policy"
	}

	var current gatewayv1.PolicyStatus
	if raw, ok := policy.Object["status"].(map[string]any); ok {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &current); err != nil {
			return err
		}
	}
	if len(current.Ancestors) == 1 && len(current.Ancestors[0].Conditions) == 1 {
		accepted.LastTransitionTime = current.Ancestors[0].Conditions[0].LastTransitionTime
	}

	want := gatewayv1.PolicyStatus{Ancestors: []gatewayv1.PolicyAncestorStatus{{
		AncestorRef: gatewayv1.ParentReference{
			Group:     new(gatewayv1.Group(gatewayv1.GroupName)),
			Kind:      new(gatewayv1.Kind("Gateway")),
			Namespace: new(gatewayv1.Namespace("gateway-system")),
			Name:      "public",
		},
		ControllerName: fakeGatewayController,
		Conditions:     []metav1.Condition{accepted},
	}}}
	if equality.Semantic.DeepEqual(current, want) {
		return nil
	}

	status, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&want)
	if err != nil {
		return err
	}
	policy.Object["status"] = status
	return c.Status().Update(ctx, policy)
}
