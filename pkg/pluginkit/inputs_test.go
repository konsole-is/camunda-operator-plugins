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

package pluginkit_test

import (
	"context"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// The registration of the input plugin, and the name of its requests.
const (
	inputRegistrationName = "input-plugin"
	inputRequestName      = "orders-input"
)

var _ = Describe("a required input", func() {
	It("reports MissingInput and does not call the plugin while a required input has no value", func() {
		request := createInputRequest("input-missing", nil)

		Eventually(func(g Gomega) {
			for _, conditionType := range []string{pluginsv1alpha1.ConditionOutputsReady, v1.ConditionReady} {
				cond := condition(g, request, conditionType)
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(pluginkit.ReasonMissingInput))
				g.Expect(cond.Message).To(Equal(
					"the request has no value for the required input key (from encryption-key/keyRef)"))
			}
		}).Should(Succeed())
		Consistently(func() int { return inputs.reconcileCount(request.Namespace) }).
			WithTimeout(2 * time.Second).Should(BeZero())
		Expect(controllerutil.ContainsFinalizer(readRequest(Default, request), pluginsv1alpha1.FinalizerCleanup)).
			To(BeFalse())

		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Inputs = map[string]string{"key": "arn:aws:kms:eu-west-1:1:key/k"}
		})
		waitReady(request)
		Expect(inputs.reconcileCount(request.Namespace)).To(BeNumerically(">", 0))
	})

	It("keeps the finalizer and the outputs of a served request that loses a required input", func() {
		request := createInputRequest("input-lost", map[string]string{"key": "arn:aws:kms:eu-west-1:1:key/k"})
		waitReady(request)

		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Inputs = nil })
		Eventually(func(g Gomega) {
			g.Expect(condition(g, request, v1.ConditionReady).Reason).To(Equal(pluginkit.ReasonMissingInput))
		}).Should(Succeed())
		current := readRequest(Default, request)
		Expect(current.Status.Outputs).To(Equal(map[string]string{"value": "served"}))
		Expect(controllerutil.ContainsFinalizer(current, pluginsv1alpha1.FinalizerCleanup)).To(BeTrue())

		Expect(k8s.Delete(ctx, request)).To(Succeed())
		Eventually(gone(request)).Should(BeTrue())
		Expect(inputs.deleteCount(request.Namespace)).To(Equal(1))
	})
})

// inputPlugin serves the registration with a required input. It counts the
// calls of Reconcile and of Delete, by namespace.
type inputPlugin struct {
	mu         sync.Mutex
	reconciles map[string]int
	deletes    map[string]int
}

func (p *inputPlugin) reconcileCount(namespace string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reconciles[namespace]
}

func (p *inputPlugin) deleteCount(namespace string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deletes[namespace]
}

func (p *inputPlugin) Reconcile(
	_ context.Context,
	req pluginkit.Request[struct{}, map[string]string],
) (pluginkit.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.reconciles == nil {
		p.reconciles = map[string]int{}
	}
	p.reconciles[req.Object.Namespace]++
	return pluginkit.Result{Outputs: map[string]string{"value": "served"}}, nil
}

func (p *inputPlugin) Delete(_ context.Context, req pluginkit.Request[struct{}, map[string]string]) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.deletes == nil {
		p.deletes = map[string]int{}
	}
	p.deletes[req.Object.Namespace]++
	return nil
}

func (p *inputPlugin) Orphan(context.Context, pluginkit.Request[struct{}, map[string]string]) error {
	return nil
}

func inputRegistration() pluginsv1alpha1.PluginRegistration {
	return pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: inputRegistrationName},
		Spec: pluginsv1alpha1.PluginRegistrationSpec{
			Sections:         []string{"test"},
			Label:            "Input plugin",
			Mode:             pluginsv1alpha1.ModeOptional,
			Inputs:           []pluginsv1alpha1.InputDecl{{Name: "key", From: "encryption-key/keyRef", Required: true}},
			Outputs:          []pluginsv1alpha1.OutputDecl{{Key: "value"}},
			DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{pluginsv1alpha1.DeletionPolicyDelete},
		},
	}
}

// createInputRequest creates the namespace ns and a request in it for the
// input plugin, with inputs, and returns the request.
func createInputRequest(ns string, inputs map[string]string) *pluginsv1alpha1.PluginRequest {
	GinkgoHelper()
	return createRequest(ns, func(r *pluginsv1alpha1.PluginRequest) {
		r.Name = inputRequestName
		r.Labels[pluginsv1alpha1.LabelRegistration] = inputRegistrationName
		r.Spec.Registration = inputRegistrationName
		r.Spec.Parameters = nil
		r.Spec.Inputs = inputs
	})
}
