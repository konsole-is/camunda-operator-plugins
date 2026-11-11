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
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// driftQuiet is longer than two resyncs of rolePodIdentity.
const driftQuiet = 5 * time.Second

// readyDriftRequest creates a request of rolePodIdentity with params in a
// namespace of its own, waits until it is Ready and its role has no more
// calls, and returns the request and its role.
func readyDriftRequest(params string) (*pluginsv1alpha1.PluginRequest, string) {
	GinkgoHelper()
	ns := newNamespace(rolePodIdentity)
	req := connectorsRequest(rolePodIdentity, ns, params)
	Expect(rolePodIdentity.k8s.Create(ctx, req)).To(Succeed())
	waitReady(rolePodIdentity, req)
	name := rolePodIdentity.roleName(ns)
	settledRoleCalls(rolePodIdentity, name)
	return req, name
}

// expectOneDriftCorrected waits for one DriftCorrected event of req whose
// note holds each of notes, and the summary item of the drift, and then
// checks that no other DriftCorrected event follows.
func expectOneDriftCorrected(req *pluginsv1alpha1.PluginRequest, notes ...string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		g.Expect(eventCount(g, rolePodIdentity, req.Namespace, "DriftCorrected")).To(Equal(1))
		matchers := make([]gomegatypes.GomegaMatcher, 0, len(notes))
		for _, n := range notes {
			matchers = append(matchers, ContainSubstring(n))
		}
		g.Expect(eventNotes(g, rolePodIdentity, req.Namespace, "DriftCorrected")).To(ConsistOf(And(matchers...)))
		got := read(g, rolePodIdentity, req)
		g.Expect(got.Status.Summary).To(ContainElement(HaveField("Label", "Last drift corrected")))
	}, timeout).Should(Succeed())

	Consistently(func(g Gomega) {
		g.Expect(eventCount(g, rolePodIdentity, req.Namespace, "DriftCorrected")).To(Equal(1))
	}, driftQuiet).Should(Succeed())
}

// expectNoDriftCorrected checks for longer than two resyncs that req records
// no DriftCorrected event.
func expectNoDriftCorrected(req *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Consistently(func(g Gomega) {
		g.Expect(eventNotes(g, rolePodIdentity, req.Namespace, "DriftCorrected")).To(BeEmpty())
	}, driftQuiet).Should(Succeed())
}

var _ = Describe("drift of a Connectors role", func() {
	It("records one DriftCorrected for a policy detached in AWS", func() {
		req, name := readyDriftRequest(managedParams(policyA))

		_, err := rolePodIdentity.iam.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
			RoleName:  aws.String(name),
			PolicyArn: aws.String(policyA),
		})
		Expect(err).NotTo(HaveOccurred())

		expectOneDriftCorrected(req, "the managed policy "+policyA+" of the IAM role "+name)
		iamRole, _ := rolePodIdentity.iam.Role(name)
		Expect(iamRole.Attached).To(ConsistOf(policyA))
	})

	It("records one DriftCorrected for an inline policy changed in AWS", func() {
		req, name := readyDriftRequest(`{"inlinePolicy":` + sqsPolicy + `}`)
		before, _ := rolePodIdentity.iam.Role(name)

		_, err := rolePodIdentity.iam.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
			RoleName:   aws.String(name),
			PolicyName: aws.String("connectors"),
			PolicyDocument: aws.String(
				`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`,
			),
		})
		Expect(err).NotTo(HaveOccurred())

		expectOneDriftCorrected(req, "the inline policy connectors of the IAM role "+name)
		after, _ := rolePodIdentity.iam.Role(name)
		Expect(after.InlinePolicies["connectors"]).To(MatchJSON(before.InlinePolicies["connectors"]))
	})

	It("records one DriftCorrected for a deleted association", func() {
		req, _ := readyDriftRequest(managedParams(policyA))
		var id string
		for _, a := range rolePodIdentity.eks.Associations() {
			if a.Namespace == req.Namespace {
				id = a.ID
			}
		}
		Expect(id).NotTo(BeEmpty())

		_, err := rolePodIdentity.eks.DeletePodIdentityAssociation(ctx, &eks.DeletePodIdentityAssociationInput{
			ClusterName:   aws.String(eksCluster),
			AssociationId: aws.String(id),
		})
		Expect(err).NotTo(HaveOccurred())

		expectOneDriftCorrected(req, "the Pod Identity association of the ServiceAccount orders-connectors")
		Expect(associationsIn(rolePodIdentity, req.Namespace)).To(HaveLen(1))
	})

	It("records the repair of the trust policy when a later step of the pass fails", func() {
		req, name := readyDriftRequest(managedParams(policyA))

		_, err := rolePodIdentity.iam.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(name),
			PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = rolePodIdentity.iam.TagRole(ctx, &iam.TagRoleInput{
			RoleName: aws.String(name),
			Tags:     []iamtypes.Tag{{Key: aws.String(awskit.TagClaim), Value: aws.String("changed")}},
		})
		Expect(err).NotTo(HaveOccurred())
		rolePodIdentity.iam.FailNext("TagRole", errors.New("the role cannot be tagged"))

		Eventually(func(g Gomega) {
			g.Expect(eventCount(g, rolePodIdentity, req.Namespace, "DriftCorrected")).To(Equal(2))
			iamRole, _ := rolePodIdentity.iam.Role(name)
			g.Expect(iamRole.Tags).To(HaveKeyWithValue(awskit.TagClaim, "orders"))
		}, timeout).Should(Succeed())
		Expect(eventNotes(Default, rolePodIdentity, req.Namespace, "DriftCorrected")).
			To(ContainElement(ContainSubstring("the trust policy of the IAM role " + name)))
	})

	It("records one DriftCorrected for an association of a request that names no ServiceAccount", func() {
		req, name := readyDriftRequest(managedParams(policyA))
		update(rolePodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Target.ServiceAccountName = "" })
		waitReason(rolePodIdentity, req, v1.ConditionReady, "MissingServiceAccount")
		Expect(associationsIn(rolePodIdentity, req.Namespace)).To(BeEmpty())
		iamRole, _ := rolePodIdentity.iam.Role(name)

		_, err := rolePodIdentity.eks.CreatePodIdentityAssociation(ctx, &eks.CreatePodIdentityAssociationInput{
			ClusterName:    aws.String(eksCluster),
			Namespace:      aws.String(req.Namespace),
			ServiceAccount: aws.String("orders-connectors"),
			RoleArn:        aws.String(iamRole.ARN),
			Tags:           awskit.RequestTags(req.Namespace, "orders", req.Name),
		})
		Expect(err).NotTo(HaveOccurred())

		expectOneDriftCorrected(req, "the Pod Identity association of the ServiceAccount orders-connectors")
		Expect(associationsIn(rolePodIdentity, req.Namespace)).To(BeEmpty())
	})

	It("records no DriftCorrected for a change of the parameters", func() {
		req, name := readyDriftRequest(managedParams(policyA))

		update(rolePodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(managedParams(policyB))
		})

		Eventually(func(g Gomega) {
			iamRole, _ := rolePodIdentity.iam.Role(name)
			g.Expect(iamRole.Attached).To(ConsistOf(policyB))
		}, timeout).Should(Succeed())
		expectNoDriftCorrected(req)
	})

	It("records no drift when a change of the parameters needs a retry", func() {
		req, name := readyDriftRequest(managedParams(policyA))
		rolePodIdentity.iam.FailNext("AttachRolePolicy", errors.New("the policy cannot be attached"))

		update(rolePodIdentity, req, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = raw(managedParams(policyA, policyC))
		})

		Eventually(func(g Gomega) {
			iamRole, _ := rolePodIdentity.iam.Role(name)
			g.Expect(iamRole.Attached).To(ConsistOf(policyA, policyC))
		}, timeout).Should(Succeed())
		Expect(strings.Count(strings.Join(rolePodIdentity.iam.RoleCalls(name), ","), "AttachRolePolicy")).
			To(BeNumerically(">=", 2), "the first attach failed")
		expectNoDriftCorrected(req)
	})
})
