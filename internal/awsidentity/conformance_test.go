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

package awsidentity_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
	"github.com/konsole-is/camunda-operator/conformance"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// envtestSkipReason is why a conformance run in envtest skips the checks
// that read the Pods of the plugin.
const envtestSkipReason = "envtest runs the controller in the test process, with no pod"

// acceptRegistration writes Accepted=True on the registration name, in place
// of the registry controller of the operator. It writes again until a read
// shows both Accepted=True and the Ready=True of the plugin, so that a status
// write of the plugin does not hide it.
func acceptRegistration(name string) {
	Eventually(func(g Gomega) {
		var reg pluginsv1alpha1.PluginRegistration
		g.Expect(k8s.Get(ctx, client.ObjectKey{Name: name}, &reg)).To(Succeed())
		g.Expect(meta.IsStatusConditionTrue(reg.Status.Conditions, v1.ConditionReady)).To(BeTrue())

		if !meta.IsStatusConditionTrue(reg.Status.Conditions, pluginsv1alpha1.ConditionAccepted) {
			meta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
				Type:    pluginsv1alpha1.ConditionAccepted,
				Status:  metav1.ConditionTrue,
				Reason:  v1.ReasonHealthy,
				Message: "The suite accepts the registration",
			})
			g.Expect(k8s.Status().Update(ctx, &reg)).To(Succeed())
		}

		var again pluginsv1alpha1.PluginRegistration
		g.Expect(k8s.Get(ctx, client.ObjectKey{Name: name}, &again)).To(Succeed())
		g.Expect(meta.IsStatusConditionTrue(again.Status.Conditions, pluginsv1alpha1.ConditionAccepted)).To(BeTrue())
		g.Expect(meta.IsStatusConditionTrue(again.Status.Conditions, v1.ConditionReady)).To(BeTrue())
	}, time.Minute).Should(Succeed())
}

// runConformance runs the suite against the registration of h with one
// cluster case, and returns its namespace.
func runConformance(h *hub, target pluginsv1alpha1.TargetRef) string {
	ns := "conformance-" + h.registration
	acceptRegistration(h.registration)
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())

	results, err := conformance.Execute(ctx, conformance.Config{
		Registration: h.registration,
		Namespace:    ns,
		Cases:        []conformance.Case{{Name: "cluster", Target: target}},
		Timeouts:     conformance.Timeouts{OutputsReady: time.Minute, Ready: time.Minute, Deletion: time.Minute},
		Skip: []conformance.Skip{
			{ID: conformance.ReqLogs, Reason: envtestSkipReason},
			{ID: conformance.OrderRegistrationFirst, Reason: envtestSkipReason},
		},
		RESTConfig: restConfig,
	})
	Expect(err).NotTo(HaveOccurred())

	var skipped []conformance.CheckID
	for _, r := range results {
		Expect(r.Outcome).NotTo(Equal(conformance.OutcomeFailed), "%s: %s", r.ID, r.Message)
		if r.Outcome == conformance.OutcomeSkipped {
			skipped = append(skipped, r.ID)
		}
	}
	// DELETE-ROUTES skips itself: the registration does not serve the access
	// section. The grant checks skip themselves: the registration of the hub
	// has no grant templates.
	Expect(skipped).To(ConsistOf(
		conformance.ReqLogs,
		conformance.OrderRegistrationFirst,
		conformance.DeleteRoutes,
		conformance.GrantMatch,
		conformance.GrantRequires,
		conformance.GrantAdmission,
		conformance.GrantReady,
	))
	for _, id := range []conformance.CheckID{conformance.DeleteDelete, conformance.DeleteOrphan} {
		Expect(results).To(ContainElement(And(HaveField("ID", id), HaveField("Outcome", conformance.OutcomePassed))))
	}

	return ns
}

// rolesOf returns the roles of h whose namespace tag is namespace.
func rolesOf(h *hub, namespace string) []string {
	var names []string
	for _, r := range h.iam.Roles() {
		if r.Tags[awskit.TagNamespace] == namespace {
			names = append(names, r.Name)
		}
	}
	return names
}

var _ = Describe("the conformance suite", func() {
	It("passes against the aws-irsa registration and keeps only the orphaned role", func() {
		ns := runConformance(irsa, pluginsv1alpha1.TargetRef{
			Kind:                      "CamundaCluster",
			Name:                      "orders",
			ServiceAccountName:        "orders-camunda",
			AdditionalServiceAccounts: []string{"orders-es-es"},
		})

		Expect(rolesOf(irsa, ns)).To(HaveLen(1))
	})

	It("passes against the aws-pod-identity registration and leaves no association", func() {
		// The suite gives the request of each deletion policy a ServiceAccount
		// of its own, but not its additional ones. Two claims cannot share
		// an association, so this case names none.
		ns := runConformance(podIdentity, pluginsv1alpha1.TargetRef{
			Kind:               "CamundaCluster",
			Name:               "orders",
			ServiceAccountName: "orders-camunda",
		})

		Expect(rolesOf(podIdentity, ns)).To(HaveLen(1))
		for _, a := range podIdentity.eks.Associations() {
			Expect(a.Namespace).NotTo(Equal(ns))
		}
	})
})
