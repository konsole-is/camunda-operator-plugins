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

package awss3_test

import (
	"strings"
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
	It("passes against aws-s3", func() {
		const ns = "conformance-aws-s3"
		acceptRegistration(registration)
		Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		target := pluginsv1alpha1.TargetRef{
			Kind:               "CamundaCluster",
			Name:               "orders",
			ServiceAccountName: "orders-camunda",
		}

		results, err := conformance.Execute(ctx, conformance.Config{
			Registration: registration,
			Namespace:    ns,
			Cases: []conformance.Case{
				{Name: "with-key", Inputs: map[string]string{"key": keyARN, "principal": roleARN}, Target: target},
				{Name: "without-key", Target: target},
			},
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
		// access section. GRANT-ADMISSION skips itself: envtest has no
		// admission policy.
		Expect(skipped).To(ConsistOf(
			conformance.ReqLogs,
			conformance.OrderRegistrationFirst,
			conformance.DeleteRoutes,
			conformance.GrantAdmission,
		))
		for _, id := range []conformance.CheckID{
			conformance.DeleteDelete,
			conformance.DeleteOrphan,
			conformance.GrantMatch,
			conformance.GrantRequires,
			conformance.GrantReady,
		} {
			Expect(
				results,
			).To(ContainElement(And(HaveField("ID", id), HaveField("Outcome", conformance.OutcomePassed))))
		}

		// Each case runs once with Delete and once with Orphan. Only the
		// orphaned requests leave their bucket, released from the request.
		var kept []string
		for _, b := range fake.Buckets() {
			if strings.HasPrefix(b.Name, "camunda-"+ns+"-") {
				kept = append(kept, b.Name)
				for key := range b.Tags {
					Expect(key).NotTo(HavePrefix("camunda.io/"), "the bucket %s", b.Name)
				}
			}
		}
		Expect(kept).To(HaveLen(2))
	})
})
