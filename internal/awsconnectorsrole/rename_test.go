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
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

// newPrefix is the role name prefix that the admin changes to.
const newPrefix = "team-b-connectors"

// renameSetup is a request that was Ready on a manager with the prefix
// rolePrefix, and that manager stopped, before a manager with newPrefix takes
// over the registration and the fakes.
type renameSetup struct {
	mode      awskit.Mode
	namespace string
	iam       *awsfake.IAM
	eks       *awsfake.EKS
	request   *pluginsv1alpha1.PluginRequest
	// oldRole is the role of the request on the stopped manager.
	oldRole awsfake.Role
}

func newRenameSetup(mode awskit.Mode) renameSetup {
	s := renameSetup{mode: mode, iam: awsfake.NewIAM(), eks: awsfake.NewEKS()}

	old, stop := startConnectors(mode, rolePrefix, s.iam, s.eks)
	s.namespace = newNamespace(old)
	s.request = connectorsRequest(old, s.namespace, paramsOf(old))
	Expect(old.k8s.Create(ctx, s.request)).To(Succeed())
	waitReady(old, s.request)
	stop()

	var ok bool
	s.oldRole, ok = s.iam.Role(old.roleName(s.namespace))
	Expect(ok).To(BeTrue(), "the old role exists")
	return s
}

// startNewManager starts the manager with newPrefix on the fakes of s. At
// the end of the spec, it deletes the request while the manager runs, so
// that the managers of later specs find no request of s, and then stops it.
func (s renameSetup) startNewManager() *connectors {
	c, stop := startConnectors(s.mode, newPrefix, s.iam, s.eks)
	DeferCleanup(stop)
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(c.k8s.Delete(ctx, s.request))).To(Succeed())
		Eventually(func(g Gomega) {
			err := c.k8s.Get(ctx, client.ObjectKeyFromObject(s.request), &pluginsv1alpha1.PluginRequest{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the request is gone: %v", err)
		}, timeout).Should(Succeed())
	})
	return c
}

// setParameters writes params into the request of s while no manager runs.
func (s renameSetup) setParameters(params string) {
	Eventually(func(g Gomega) {
		got := &pluginsv1alpha1.PluginRequest{}
		g.Expect(renameCluster.k8s.Get(ctx, client.ObjectKeyFromObject(s.request), got)).To(Succeed())
		got.Spec.Parameters = raw(params)
		g.Expect(renameCluster.k8s.Update(ctx, got)).To(Succeed())
	}, timeout).Should(Succeed())
}

// expectReadyOnTheNewRole waits until the request of s is Ready with the
// outputs and the policies of the role of c, the old role is gone from
// status.resources, and there is one RoleDeleted event for each name of
// deleted, which also names the role of c.
func (s renameSetup) expectReadyOnTheNewRole(c *connectors, deleted ...string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		role, ok := s.iam.Role(c.roleName(s.namespace))
		g.Expect(ok).To(BeTrue(), "the new role exists")
		g.Expect(role.Attached).To(ConsistOf(policyA))
		g.Expect(role.InlinePolicies).To(HaveKey("connectors"))

		got := read(g, c, s.request)
		g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		g.Expect(got.Status.Outputs).To(HaveKeyWithValue("principal", role.ARN))
		g.Expect(got.Status.Resources).NotTo(ContainElement(HaveField("Name", s.oldRole.Name)))

		matchers := make([]any, 0, len(deleted))
		for _, name := range deleted {
			matchers = append(matchers, And(ContainSubstring(name), ContainSubstring(role.Name)))
		}
		g.Expect(eventNotes(g, c, s.namespace, "RoleDeleted")).To(ConsistOf(matchers...))
	}, timeout).Should(Succeed())
}

var _ = Describe("a change of the role name prefix", Ordered, Serial, func() {
	DescribeTable("deletes the old role after the outputs name the new one",
		func(mode awskit.Mode) {
			s := newRenameSetup(mode)
			c := s.startNewManager()

			s.expectReadyOnTheNewRole(c, s.oldRole.Name)
			_, ok := s.iam.Role(s.oldRole.Name)
			Expect(ok).To(BeFalse(), "the old role is gone")

			if mode == awskit.ModePodIdentity {
				role, _ := s.iam.Role(c.roleName(s.namespace))
				Expect(associationsIn(c, s.namespace)).To(ConsistOf("orders-connectors=" + role.ARN))
				Expect(s.eks.Calls()).To(ContainElement("UpdatePodIdentityAssociation"))
			}
		},
		Entry("IRSA", awskit.ModeIRSA),
		Entry("Pod Identity", awskit.ModePodIdentity),
	)

	It("deletes both roles when the request is deleted after a change of the prefix", func() {
		s := newRenameSetup(awskit.ModeIRSA)
		_, _, err := awskit.EnsureRole(ctx, s.iam, awskit.RoleSpec{
			Name:        awskit.RoleName(newPrefix, issuer, s.namespace, "orders"),
			TrustPolicy: []byte(`{"Version":"2012-10-17","Statement":[]}`),
			Tags: awskit.ExtraTags(nil).OwnerTags(
				s.namespace,
				"orders",
				s.request.Name,
				string(s.request.UID),
			),
		})
		Expect(err).NotTo(HaveOccurred())
		// The finalizer holds the request until a manager runs Delete.
		Expect(renameCluster.k8s.Delete(ctx, s.request)).To(Succeed())

		c := s.startNewManager()

		Eventually(func(g Gomega) {
			err := c.k8s.Get(ctx, client.ObjectKeyFromObject(s.request), &pluginsv1alpha1.PluginRequest{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the request is gone: %v", err)
			g.Expect(rolesOf(c, s.namespace)).To(BeEmpty())
			g.Expect(eventCount(g, c, s.namespace, "RoleDeleted")).To(Equal(2))
		}, timeout).Should(Succeed())
	})

	It("keeps an old role whose UID tag names another request", func() {
		s := newRenameSetup(awskit.ModeIRSA)
		_, err := s.iam.TagRole(ctx, &iam.TagRoleInput{
			RoleName: aws.String(s.oldRole.Name),
			Tags:     []iamtypes.Tag{{Key: aws.String(awskit.TagRequestUID), Value: aws.String("another-uid")}},
		})
		Expect(err).NotTo(HaveOccurred())

		c := s.startNewManager()

		s.expectReadyOnTheNewRole(c)
		Eventually(func(g Gomega) {
			g.Expect(eventNotes(g, c, s.namespace, "RoleKept")).To(ConsistOf(
				And(ContainSubstring(s.oldRole.Name), ContainSubstring("another-uid")),
			))
		}, timeout).Should(Succeed())
		old, ok := s.iam.Role(s.oldRole.Name)
		Expect(ok).To(BeTrue(), "the old role stays")
		Expect(old.Attached).To(ConsistOf(policyA))
		Expect(s.iam.RoleCalls(s.oldRole.Name)).NotTo(ContainElement("DeleteRole"))
	})

	It("keeps the old role through a pass with invalid parameters", func() {
		s := newRenameSetup(awskit.ModeIRSA)
		valid := paramsOf(role)
		s.setParameters(`{"inlinePolicy":{"Version":"2012-10-17","Statement":[{"Sid":"` +
			strings.Repeat("a", 10300) + `","Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}}`)

		c := s.startNewManager()

		waitReason(c, s.request, pluginsv1alpha1.ConditionOutputsReady, pluginsv1alpha1.ReasonInvalidParameters)
		got := read(Default, c, s.request)
		Expect(got.Status.Resources).To(ContainElement(HaveField("Name", s.oldRole.Name)))
		_, ok := s.iam.Role(s.oldRole.Name)
		Expect(ok).To(BeTrue(), "the old role stays")

		update(c, s.request, func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Parameters = raw(valid) })

		s.expectReadyOnTheNewRole(c, s.oldRole.Name)
		_, ok = s.iam.Role(s.oldRole.Name)
		Expect(ok).To(BeFalse(), "the old role is gone")
	})
})
