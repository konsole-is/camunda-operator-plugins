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

package awskms_test

import (
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	"github.com/konsole-is/camunda-operator/conformance"
)

// envtestSkipReason is why a conformance run in envtest skips the checks
// that read the Pods of the plugin.
const envtestSkipReason = "envtest runs the controller in the test process, with no pod"

// runConformance runs the suite against registration with one cluster case
// of params, and returns its namespace.
func runConformance(registration string, params *apiextensionsv1.JSON) string {
	ns := "conformance-" + registration
	acceptRegistration(registration)
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())

	results, err := conformance.Execute(ctx, conformance.Config{
		Registration: registration,
		Namespace:    ns,
		Cases: []conformance.Case{{
			Name:       "cluster",
			Parameters: params,
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

// expectOnlyTheOrphanedKeyKept checks that in each of regions one key of
// namespace is Enabled, the key of the one alias left in namespace, and
// that each other key of namespace is pending deletion.
func expectOnlyTheOrphanedKeyKept(namespace string, regions ...string) {
	GinkgoHelper()
	out, err := fake.ListAliases(ctx, &kms.ListAliasesInput{})
	Expect(err).NotTo(HaveOccurred())

	var kept []string
	for _, a := range out.Aliases {
		if strings.HasPrefix(aws.ToString(a.AliasName), "alias/camunda/"+namespace+"/") {
			kept = append(kept, aws.ToString(a.TargetKeyId))
		}
	}
	Expect(kept).To(HaveLen(1), "one alias of the orphaned key")

	for _, region := range regions {
		key, ok := fake.Key(region, kept[0])
		Expect(ok).To(BeTrue(), "the orphaned key exists in %s", region)
		Expect(key.State).To(Equal(types.KeyStateEnabled))

		deleted := keysOf(namespace, region)
		Expect(deleted).NotTo(BeEmpty())
		for _, k := range deleted {
			Expect(k.ID).NotTo(Equal(kept[0]))
			Expect(k.State).To(BeElementOf(types.KeyStatePendingDeletion, types.KeyStatePendingReplicaDeletion))
		}
	}
}

var _ = Describe("the conformance suite", func() {
	It("passes against aws-kms-key and keeps only the orphaned key", func() {
		ns := runConformance(singleRegion, nil)

		expectOnlyTheOrphanedKeyKept(ns, home)
	})

	It("passes against aws-kms-key-multi-region and keeps only the orphaned key and its replica", func() {
		ns := runConformance(multiRegion, raw(`{"replicaRegions":["eu-central-1"],"pendingWindowDays":7}`))

		expectOnlyTheOrphanedKeyKept(ns, home, replica)
	})
})
