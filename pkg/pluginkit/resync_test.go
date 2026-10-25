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
	"errors"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// The registration of the drift plugin, and the value that it keeps.
const (
	driftRegistrationName = "drift-plugin"
	wantedValue           = "wanted"
)

// driftResync is the Resync of the drift plugin.
const driftResync = time.Second

// driftPlugin keeps one value outside the cluster for each request, by
// namespace. It reports each value that it sets back as corrected.
type driftPlugin struct {
	mu     sync.Mutex
	values map[string]string
	// err is the error of Reconcile, which it returns with the corrections.
	err error
}

func (p *driftPlugin) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *driftPlugin) change(namespace, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.values[namespace] = value
}

func (p *driftPlugin) value(namespace string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.values[namespace]
}

func (p *driftPlugin) Reconcile(
	_ context.Context,
	req pluginkit.Request[struct{}, map[string]string],
) (pluginkit.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var res pluginkit.Result
	value, ok := p.values[req.Object.Namespace]
	if ok && value != wantedValue {
		res.Corrected = []string{"set the value " + value + " back to " + wantedValue}
	}
	p.values[req.Object.Namespace] = wantedValue
	return res, p.err
}

func (p *driftPlugin) Delete(context.Context, pluginkit.Request[struct{}, map[string]string]) error {
	return nil
}

func (p *driftPlugin) Orphan(context.Context, pluginkit.Request[struct{}, map[string]string]) error {
	return nil
}

func driftRegistration() pluginsv1alpha1.PluginRegistration {
	return pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: driftRegistrationName},
		Spec: pluginsv1alpha1.PluginRegistrationSpec{
			Sections:         []string{"test"},
			Label:            "Drift plugin",
			Mode:             pluginsv1alpha1.ModeOptional,
			DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{pluginsv1alpha1.DeletionPolicyDelete},
		},
	}
}

// forDriftPlugin makes a request of createRequest a request of the drift
// plugin.
func forDriftPlugin(r *pluginsv1alpha1.PluginRequest) {
	r.Labels[pluginsv1alpha1.LabelRegistration] = driftRegistrationName
	r.Spec.Registration = driftRegistrationName
	r.Spec.Parameters = nil
}

// driftEvents returns the DriftCorrected events on request.
func driftEvents(g Gomega, request *pluginsv1alpha1.PluginRequest) []eventsv1.Event {
	current := readRequest(g, request)
	var list eventsv1.EventList
	g.Expect(k8s.List(ctx, &list, client.InNamespace(request.Namespace))).To(Succeed())

	var found []eventsv1.Event
	for _, e := range list.Items {
		if e.Regarding.UID == current.UID && e.Reason == "DriftCorrected" {
			found = append(found, e)
		}
	}
	return found
}

// settle waits until a reconcile of request writes nothing for two resyncs,
// so that no reconcile of an earlier write is still in the queue.
func settle(request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		before := readRequest(g, request).ResourceVersion
		time.Sleep(2 * driftResync)
		g.Expect(readRequest(g, request).ResourceVersion).To(Equal(before))
	}).Should(Succeed())
}

var _ = Describe("the resync", func() {
	It("corrects a change outside the cluster with no change in the cluster", func() {
		request := createRequest("drift", forDriftPlugin)
		waitReady(request)
		settle(request)
		Expect(drift.value(request.Namespace)).To(Equal(wantedValue))
		Expect(driftEvents(Default, request)).To(BeEmpty(), "the first create is not drift")

		drift.change(request.Namespace, "changed")

		Eventually(func(g Gomega) {
			g.Expect(drift.value(request.Namespace)).To(Equal(wantedValue))
			g.Expect(readRequest(g, request).Status.Summary).To(ContainElement(SatisfyAll(
				HaveField("Label", "Last drift corrected"), HaveField("Value", Not(BeEmpty())),
			)))
			events := driftEvents(g, request)
			g.Expect(events).To(HaveLen(1))
			g.Expect(events[0].Type).To(Equal("Normal"))
			g.Expect(events[0].Note).To(ContainSubstring("set the value changed back to wanted"))
		}).Should(Succeed())

		Consistently(func(g Gomega) {
			g.Expect(driftEvents(g, request)).To(HaveLen(1))
		}).WithTimeout(3 * driftResync).Should(Succeed())
	})

	It("records a repair of a reconcile that then fails", func() {
		request := createRequest("drift-error", forDriftPlugin)
		waitReady(request)
		settle(request)
		drift.setErr(errors.New("the cloud refused the next step"))
		DeferCleanup(func() { drift.setErr(nil) })

		drift.change(request.Namespace, "changed")

		Eventually(func(g Gomega) {
			g.Expect(condition(g, request, "Ready").Reason).To(Equal(pluginkit.ReasonReconcileError))
			g.Expect(readRequest(g, request).Status.Summary).
				To(ContainElement(HaveField("Label", "Last drift corrected")))
			g.Expect(driftEvents(g, request)).To(HaveLen(1))
		}).Should(Succeed())
	})
})
