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
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func readRegistration(g Gomega) *pluginsv1alpha1.PluginRegistration {
	var registration pluginsv1alpha1.PluginRegistration
	g.Expect(k8s.Get(ctx, client.ObjectKey{Name: testRegistrationName}, &registration)).To(Succeed())
	return &registration
}

var _ = Describe("the registration publisher", func() {
	It("applies each registration with the controller and the ServiceAccount of the plugin", func() {
		Eventually(func(g Gomega) {
			registration := readRegistration(g)
			g.Expect(registration.Spec.Controller).To(Equal(pluginsv1alpha1.ControllerInfo{
				Name: "test-plugin", Version: "1.2.3",
			}))
			g.Expect(registration.Spec.ServiceAccount).To(Equal(pluginsv1alpha1.ServiceAccountRef{
				Namespace: "plugins", Name: "test-plugin",
			}))
			g.Expect(registration.Spec.Label).To(Equal("Test plugin"))
			g.Expect(registration.ManagedFields).To(ContainElement(HaveField("Manager", pluginkit.FieldManager)))
		}).Should(Succeed())
	})

	It("sets Ready from the health check and keeps Accepted", func() {
		DeferCleanup(func() { health.set(nil) })

		// No registry controller runs here, so the spec writes Accepted.
		Eventually(func(g Gomega) {
			registration := readRegistration(g)
			meta.SetStatusCondition(&registration.Status.Conditions, metav1.Condition{
				Type: pluginsv1alpha1.ConditionAccepted, Status: metav1.ConditionTrue, Reason: "Healthy",
			})
			g.Expect(k8s.Status().Update(ctx, registration)).To(Succeed())
		}).Should(Succeed())

		// A Ready write from a read older than the Accepted write conflicts,
		// so a later Ready beside Accepted in one read proves that the
		// publisher read Accepted.
		health.set(errors.New("the credentials expired"))
		Eventually(func(g Gomega) {
			conditions := readRegistration(g).Status.Conditions
			ready := meta.FindStatusCondition(conditions, "Ready")
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(pluginkit.ReasonUnhealthy))
			g.Expect(ready.Message).To(Equal("the credentials expired"))
			g.Expect(meta.IsStatusConditionTrue(conditions, pluginsv1alpha1.ConditionAccepted)).To(BeTrue())
		}).Should(Succeed())

		health.set(nil)
		Eventually(func(g Gomega) {
			registration := readRegistration(g)
			ready := meta.FindStatusCondition(registration.Status.Conditions, "Ready")
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(ready.Reason).To(Equal("Healthy"))
			g.Expect(meta.IsStatusConditionTrue(registration.Status.Conditions, pluginsv1alpha1.ConditionAccepted)).
				To(BeTrue())
			g.Expect(registration.Status.ObservedGeneration).To(Equal(registration.Generation))
		}).Should(Succeed())
	})
})
