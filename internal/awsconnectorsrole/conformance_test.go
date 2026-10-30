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

package awsconnectorsrole_test

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
)

// envtestSkipReason is why a conformance run in envtest skips the checks
// that read the Pods of the plugin.
const envtestSkipReason = "envtest runs the controller in the test process, with no pod"

// acceptRegistration writes Accepted=True on the registration of c, in place
// of the registry controller of the operator. It writes again until a read
// shows both Accepted=True and the Ready=True of the plugin, so that a status
// write of the plugin does not hide it.
func acceptRegistration(c *connectors) {
	Eventually(func(g Gomega) {
		var reg pluginsv1alpha1.PluginRegistration
		g.Expect(c.k8s.Get(ctx, client.ObjectKey{Name: c.registration}, &reg)).To(Succeed())
		g.Expect(meta.IsStatusConditionTrue(reg.Status.Conditions, v1.ConditionReady)).To(BeTrue())

		if !meta.IsStatusConditionTrue(reg.Status.Conditions, pluginsv1alpha1.ConditionAccepted) {
			meta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
				Type:    pluginsv1alpha1.ConditionAccepted,
				Status:  metav1.ConditionTrue,
				Reason:  v1.ReasonHealthy,
				Message: "The suite accepts the registration",
			})
			g.Expect(c.k8s.Status().Update(ctx, &reg)).To(Succeed())
		}

		var again pluginsv1alpha1.PluginRegistration
		g.Expect(c.k8s.Get(ctx, client.ObjectKey{Name: c.registration}, &again)).To(Succeed())
		g.Expect(meta.IsStatusConditionTrue(again.Status.Conditions, pluginsv1alpha1.ConditionAccepted)).To(BeTrue())
		g.Expect(meta.IsStatusConditionTrue(again.Status.Conditions, v1.ConditionReady)).To(BeTrue())
	}, time.Minute).Should(Succeed())
}

// runConformance runs the suite against the registration of c with one
// cluster case of params, and returns its namespace.
func runConformance(c *connectors, params string) string {
	ns := "conformance-" + c.registration
	acceptRegistration(c)
	Expect(c.k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())

	results, err := conformance.Execute(ctx, conformance.Config{
		Registration: c.registration,
		Namespace:    ns,
		Cases: []conformance.Case{{
			Name:       "cluster",
			Parameters: raw(params),
			Target: pluginsv1alpha1.TargetRef{
				Kind:               "CamundaCluster",
				Name:               "orders",
				ServiceAccountName: "orders-connectors",
			},
		}},
		Timeouts: conformance.Timeouts{OutputsReady: time.Minute, Ready: time.Minute, Deletion: time.Minute},
		Skip: []conformance.Skip{
			{ID: conformance.ReqLogs, Reason: envtestSkipReason},
			{ID: conformance.OrderRegistrationFirst, Reason: envtestSkipReason},
		},
		RESTConfig: c.restConfig,
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
	// section. The grant checks skip themselves: the registrations have no
	// grant templates.
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

var _ = Describe("the conformance suite", func() {
	It("passes against aws-connectors-role and keeps only the orphaned role", func() {
		ns := runConformance(role, `{"managedPolicyArns":["`+policyA+`"],
			"inlinePolicy":{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage",
			"Resource":"arn:aws:sqs:*:*:{{ .namespace }}-*"}]}}`)

		Expect(rolesOf(role, ns)).To(HaveLen(1))
	})

	It("passes against aws-connectors-existing-role and calls no AWS API", func() {
		runConformance(existing, existingParams(existingRoleARN))

		Expect(existing.iam.Calls()).To(BeEmpty())
	})
})
