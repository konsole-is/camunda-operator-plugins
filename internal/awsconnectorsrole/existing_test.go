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
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/smithy-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

func existingParams(arn string) string {
	return `{"roleArn":"` + arn + `"}`
}

var _ = Describe("the aws-connectors-existing-role registration", func() {
	It("writes the annotation of the given role and calls no AWS API", func() {
		ns := newNamespace(existing)
		req := connectorsRequest(existing, ns, existingParams(existingRoleARN))
		Expect(existing.k8s.Create(ctx, req)).To(Succeed())
		got := waitReady(existing, req)

		Expect(got.Status.Outputs["serviceAccountAnnotations"]).
			To(MatchJSON(`{"eks.amazonaws.com/role-arn":"` + existingRoleARN + `"}`))
		Expect(got.Status.Outputs).To(HaveKeyWithValue("principal", existingRoleARN))
		Expect(got.Status.Summary[0]).To(HaveField("Value", existingRoleARN))
		Expect(got.Status.Links).To(ConsistOf(HaveField("Name", "IAM role")))
		// The manager has no boundary, so its health check calls no IAM API
		// either.
		Expect(existing.iam.Calls()).To(BeEmpty())
		Expect(existing.eks.Calls()).To(BeEmpty())
	})

	It("writes only the association in Pod Identity mode", func() {
		ns := newNamespace(existingPodIdentity)
		req := connectorsRequest(existingPodIdentity, ns, existingParams(existingRoleARN))
		Expect(existingPodIdentity.k8s.Create(ctx, req)).To(Succeed())
		got := waitReady(existingPodIdentity, req)

		Expect(associationsIn(existingPodIdentity, ns)).To(ConsistOf("orders-connectors=" + existingRoleARN))
		Expect(got.Status.Outputs["serviceAccountAnnotations"]).To(MatchJSON(`{}`))
		Expect(existingPodIdentity.iam.Calls()).To(BeEmpty())
	})

	It("moves the association when the admin changes roleArn", func() {
		const other = "arn:aws:iam::111122223333:role/connectors-2"
		ns := newNamespace(existingPodIdentity)
		req := connectorsRequest(existingPodIdentity, ns, existingParams(existingRoleARN))
		Expect(existingPodIdentity.k8s.Create(ctx, req)).To(Succeed())
		waitReady(existingPodIdentity, req)

		update(existingPodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(existingParams(other))
		})

		Eventually(func(g Gomega) {
			g.Expect(associationsIn(existingPodIdentity, ns)).To(ConsistOf("orders-connectors=" + other))
		}, timeout).Should(Succeed())
	})

	It("deletes the association of the old role when its move to a new roleArn failed", func() {
		const other = "arn:aws:iam::111122223333:role/connectors-3"
		ns := newNamespace(existingPodIdentity)
		req := connectorsRequest(existingPodIdentity, ns, existingParams(existingRoleARN))
		Expect(existingPodIdentity.k8s.Create(ctx, req)).To(Succeed())
		waitReady(existingPodIdentity, req)

		// Enough failures that every move in this spec fails. The spec
		// drains the rest, so that no other spec meets them.
		denied := &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied by the spec"}
		for range 100 {
			existingPodIdentity.eks.FailNext("UpdatePodIdentityAssociation", denied)
		}
		DeferCleanup(func() {
			for {
				_, err := existingPodIdentity.eks.UpdatePodIdentityAssociation(
					context.Background(),
					&eks.UpdatePodIdentityAssociationInput{
						ClusterName:   aws.String("none"),
						AssociationId: aws.String("none"),
					},
				)
				if !errors.Is(err, denied) {
					return
				}
			}
		})
		update(existingPodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(existingParams(other))
		})
		Expect(waitReason(existingPodIdentity, req, v1.ConditionReady, pluginkit.ReasonReconcileError)).
			To(ContainSubstring("denied by the spec"))
		Expect(associationsIn(existingPodIdentity, ns)).To(ConsistOf("orders-connectors=" + existingRoleARN))

		waitGone(existingPodIdentity, req)

		Expect(associationsIn(existingPodIdentity, ns)).To(BeEmpty())
	})

	It("keeps the association of another claim that uses the same role", func() {
		ns := newNamespace(existingPodIdentity)
		orders := connectorsRequest(existingPodIdentity, ns, existingParams(existingRoleARN))
		billing := connectorsRequest(existingPodIdentity, ns, existingParams(existingRoleARN), "billing")
		Expect(existingPodIdentity.k8s.Create(ctx, orders)).To(Succeed())
		Expect(existingPodIdentity.k8s.Create(ctx, billing)).To(Succeed())
		waitReady(existingPodIdentity, orders)
		waitReady(existingPodIdentity, billing)
		Expect(associationsIn(existingPodIdentity, ns)).To(ConsistOf(
			"orders-connectors="+existingRoleARN,
			"billing-connectors="+existingRoleARN,
		))

		// A reconcile of orders, and then its delete.
		update(existingPodIdentity, orders, func(r *pluginsv1alpha1.PluginRequest) {
			r.Annotations = map[string]string{"touch": "1"}
		})
		waitGone(existingPodIdentity, orders)

		Consistently(func(g Gomega) {
			g.Expect(associationsIn(existingPodIdentity, ns)).To(ConsistOf("billing-connectors=" + existingRoleARN))
		}, "2s").Should(Succeed())
	})

	It("refuses a roleArn that is not a role ARN", func() {
		ns := newNamespace(existing)
		req := connectorsRequest(existing, ns, existingParams("arn:aws:iam::111122223333:user/x"))
		Expect(existing.k8s.Create(ctx, req)).To(Succeed())

		waitReason(existing, req, v1.ConditionReady, pluginsv1alpha1.ReasonInvalidParameters)
	})
})
