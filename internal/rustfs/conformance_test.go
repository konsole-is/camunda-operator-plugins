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

package rustfs

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	"github.com/konsole-is/camunda-operator/conformance"
)

// envtestSkipReason is why a conformance run in envtest skips the checks
// that read the Pods of the plugin.
const envtestSkipReason = "envtest runs the controller in the test process, with no pod"

var _ = Describe("the conformance suite", Serial, func() {
	It("passes against the rustfs-bucket registration", func() {
		acceptRegistration(registration)
		before := fake.counts()

		results, err := conformance.Execute(ctx, conformance.Config{
			Registration: registration,
			Namespace:    "conformance-rustfs",
			Cases: []conformance.Case{{
				Name: "basic",
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

		// The case runs once with Delete and once with Orphan. Only the
		// orphaned request leaves its bucket, its user, and its policy.
		Expect(fake.counts()).To(Equal(before.plus(1)))
	})
})
