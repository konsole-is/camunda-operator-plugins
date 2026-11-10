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

package awsebs_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	"github.com/konsole-is/camunda-operator/conformance"
)

// envtestSkipReason is why a conformance run in envtest skips the checks
// that read the Pods of the plugin.
const envtestSkipReason = "envtest runs the controller in the test process, with no pod"

var _ = Describe("the conformance suite", func() {
	It("passes against aws-ebs-encrypted with both deletion policies", func() {
		const ns = "conformance-ebs"
		acceptRegistration(registration)
		Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())

		results, err := conformance.Execute(ctx, conformance.Config{
			Registration: registration,
			Namespace:    ns,
			Cases: []conformance.Case{{
				Name:   "cluster",
				Inputs: map[string]string{"key": "arn:aws:kms:eu-west-1:000000000000:key/x"},
				Target: pluginsv1alpha1.TargetRef{
					Kind:               "CamundaCluster",
					Name:               "orders",
					ServiceAccountName: "orders-camunda",
				},
			}},
			Timeouts: conformance.Timeouts{OutputsReady: time.Minute, Ready: time.Minute, Deletion: time.Minute},
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
		// DELETE-ROUTES skips itself: the registration does not serve the
		// access section. The grant checks skip themselves: the registration
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
	})
})
