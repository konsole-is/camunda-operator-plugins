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
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsidentity"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

const (
	// driftResync is the resync of the hubs of the drift specs.
	driftResync = 2 * time.Second
	// driftQuiet is longer than two resyncs.
	driftQuiet = 5 * time.Second
	// grantPolicy is the inline policy of the Grant of a driftSetup.
	grantPolicy = "grant-orders-backup-read"
)

// driftSetup is a Ready request of the claim orders on a hub of its own with
// the resync driftResync, and an applied Grant on its role.
type driftSetup struct {
	hub       *hub
	namespace string
	request   *pluginsv1alpha1.PluginRequest
	other     *pluginsv1alpha1.PluginRequest
	role      string
}

func newDriftSetup(mode awskit.Mode, registrationPrefix string) driftSetup {
	ns := newNamespace()
	h, stop := startHub(
		mode,
		registrationPrefix+ns,
		"camunda",
		awsfake.NewIAM(),
		awsfake.NewEKS(),
		withResync(driftResync),
	)
	DeferCleanup(stop)

	s := driftSetup{hub: h, namespace: ns, request: hubRequest(h.registration, ns), role: h.roleName(ns)}
	readyRequest(s.request)
	Expect(k8s.Create(ctx, otherRegistrationWith(ns, "read", readTemplate))).To(Succeed())
	s.other = newOtherRequest(ns, "orders-backup", "orders")
	grant := grantFor(s.other, "orders-backup-read", readFragment)
	Expect(k8s.Create(ctx, grant)).To(Succeed())
	expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)
	Expect(roleOf(Default, h, ns).InlinePolicies).To(HaveKey(grantPolicy))

	return s
}

func withResync(resync time.Duration) func(*awsidentity.Options) {
	return func(o *awsidentity.Options) { o.Resync = resync }
}

// expectOneDriftCorrected waits for one DriftCorrected event of s whose note
// holds each of notes, and the summary item of the drift, and then checks
// that no other DriftCorrected event follows.
func (s driftSetup) expectOneDriftCorrected(notes ...string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		g.Expect(eventCount(g, s.namespace, "DriftCorrected")).To(Equal(1))
		matchers := make([]gomegatypes.GomegaMatcher, 0, len(notes))
		for _, n := range notes {
			matchers = append(matchers, ContainSubstring(n))
		}
		g.Expect(eventNotes(g, s.namespace, "DriftCorrected")).To(ConsistOf(And(matchers...)))
		got := read(g, s.request.DeepCopy())
		g.Expect(got.Status.Summary).To(ContainElement(HaveField("Label", "Last drift corrected")))
	}, timeout).Should(Succeed())

	Consistently(func(g Gomega) {
		g.Expect(eventCount(g, s.namespace, "DriftCorrected")).To(Equal(1))
	}, driftQuiet).Should(Succeed())
}

// expectNoDriftCorrected checks for longer than two resyncs that s records no
// DriftCorrected event.
func expectNoDriftCorrected(namespace string) {
	GinkgoHelper()
	Consistently(func(g Gomega) {
		g.Expect(eventNotes(g, namespace, "DriftCorrected")).To(BeEmpty())
	}, driftQuiet).Should(Succeed())
}

var _ = Describe("drift of a role", Serial, func() {
	It("puts a policy back that was removed in AWS, after the resync", func() {
		s := newDriftSetup(awskit.ModeIRSA, "aws-irsa-")

		_, err := s.hub.iam.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName:   aws.String(s.role),
			PolicyName: aws.String(grantPolicy),
		})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(roleOf(g, s.hub, s.namespace).InlinePolicies).To(HaveKey(grantPolicy))
		}, timeout).Should(Succeed())
	})

	It("records one DriftCorrected for a repair", func() {
		s := newDriftSetup(awskit.ModeIRSA, "aws-irsa-")

		_, err := s.hub.iam.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName:   aws.String(s.role),
			PolicyName: aws.String(grantPolicy),
		})
		Expect(err).NotTo(HaveOccurred())

		s.expectOneDriftCorrected("the inline policy " + grantPolicy + " of the IAM role " + s.role)
	})

	It("records one DriftCorrected for a changed trust policy", func() {
		s := newDriftSetup(awskit.ModeIRSA, "aws-irsa-")

		_, err := s.hub.iam.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(s.role),
			PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
		})
		Expect(err).NotTo(HaveOccurred())

		s.expectOneDriftCorrected("the trust policy of the IAM role " + s.role)
		Expect(subjects(Default, roleOf(Default, s.hub, s.namespace))).
			To(ConsistOf("system:serviceaccount:" + s.namespace + ":orders-camunda"))
	})

	It("records the repair of the trust policy when a later step of the pass fails", func() {
		s := newDriftSetup(awskit.ModeIRSA, "aws-irsa-")

		_, err := s.hub.iam.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(s.role),
			PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
		})
		Expect(err).NotTo(HaveOccurred())
		// The Grant sync reads the inline policies after the role is repaired.
		s.hub.iam.FailNextOnRole(s.role, "ListRolePolicies", errors.New("the policies cannot be listed"))

		s.expectOneDriftCorrected("the trust policy of the IAM role " + s.role)
		Expect(roleOf(Default, s.hub, s.namespace).TrustPolicy).NotTo(ContainSubstring(`"Statement":[]`))
	})

	It("records one DriftCorrected for an association of a ServiceAccount that the request does not name", func() {
		s := newDriftSetup(awskit.ModePodIdentity, "aws-pod-identity-")

		_, err := s.hub.eks.CreatePodIdentityAssociation(ctx, &eks.CreatePodIdentityAssociationInput{
			ClusterName:    aws.String(eksCluster),
			Namespace:      aws.String(s.namespace),
			ServiceAccount: aws.String("orders-old"),
			RoleArn:        aws.String(roleOf(Default, s.hub, s.namespace).ARN),
			Tags:           awskit.RequestTags(s.namespace, "orders", s.request.Name),
		})
		Expect(err).NotTo(HaveOccurred())

		s.expectOneDriftCorrected("the Pod Identity association of the ServiceAccount orders-old")
		Expect(associationsIn(s.hub, s.namespace)).To(ConsistOf("orders-camunda"))
	})

	It("records one DriftCorrected for a deleted association", func() {
		s := newDriftSetup(awskit.ModePodIdentity, "aws-pod-identity-")
		associations := s.hub.eks.Associations()
		Expect(associations).To(HaveLen(1))

		_, err := s.hub.eks.DeletePodIdentityAssociation(ctx, &eks.DeletePodIdentityAssociationInput{
			ClusterName:   aws.String(eksCluster),
			AssociationId: aws.String(associations[0].ID),
		})
		Expect(err).NotTo(HaveOccurred())

		s.expectOneDriftCorrected("the Pod Identity association of the ServiceAccount orders-camunda")
		Expect(associationsIn(s.hub, s.namespace)).To(ConsistOf("orders-camunda"))
	})

	It("records no DriftCorrected when the ServiceAccounts come back after a pass without them failed "+
		"during a change of the prefix", func() {
		s := newRenameSetup(awskit.ModePodIdentity, "aws-pod-identity-")
		// The old role stays in the status while the new hub cannot delete it.
		s.iam.AddRoleToInstanceProfile(s.oldRole.Name, "nodes")
		h, stop := startHub(s.mode, s.registration, "team-b", s.iam, s.eks)
		var current awsfake.Role
		Eventually(func(g Gomega) {
			current = roleOf(g, h, s.namespace)
			g.Expect(current.Tags).To(HaveKey(awskit.TagDesired))
			got := read(g, s.request.DeepCopy())
			g.Expect(got.Status.Outputs).To(HaveKeyWithValue(awskit.OutputPrincipal, current.ARN))
			g.Expect(got.Status.Resources).To(ContainElement(HaveField("Name", s.oldRole.Name)))
		}, timeout).Should(Succeed())
		stop()
		s.iam.RemoveRoleFromInstanceProfile(s.oldRole.Name, "nodes")

		// The outputs name the old role again, as after a status write that
		// did not land, and the request names no ServiceAccount.
		Eventually(func(g Gomega) {
			got := read(g, s.request.DeepCopy())
			got.Status.Outputs[awskit.OutputPrincipal] = s.oldRole.ARN
			g.Expect(k8s.Status().Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		s.update(func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Target.ServiceAccountName = "" })
		s.iam.FailRole(current.Name, errors.New("the role cannot be read"))
		DeferCleanup(func() { s.iam.FailRole(current.Name, nil) })
		// Controller names are unique in the process.
		s.startNewHub(func(o *awsidentity.Options) { o.Name += "-again" })
		Eventually(func(g Gomega) {
			ready := conditionOf(read(g, s.request.DeepCopy()).Status.Conditions, v1.ConditionReady)
			g.Expect(ready.Reason).To(Equal(pluginkit.ReasonReconcileError))
			g.Expect(ready.Message).To(ContainSubstring(current.Name + ": the role cannot be read"))
		}, timeout).Should(Succeed())

		s.update(func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Target.ServiceAccountName = "orders-camunda" })
		s.iam.FailRole(current.Name, nil)

		Eventually(func(g Gomega) {
			got := read(g, s.request.DeepCopy())
			g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
			g.Expect(got.Status.Outputs).To(HaveKeyWithValue(awskit.OutputPrincipal, current.ARN))
		}, timeout).Should(Succeed())
		Expect(associationsIn(h, s.namespace)).To(ConsistOf("orders-camunda"))
		expectNoDriftCorrected(s.namespace)
	})

	It("records no DriftCorrected for a change of the request", func() {
		s := newDriftSetup(awskit.ModeIRSA, "aws-irsa-")

		Eventually(func(g Gomega) {
			got := read(g, s.request.DeepCopy())
			got.Spec.Target.AdditionalServiceAccounts = []string{"orders-es-es"}
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		Expect(k8s.Create(ctx, grantFor(s.other, "orders-backup-read-2", readFragment))).To(Succeed())

		Eventually(func(g Gomega) {
			role := roleOf(g, s.hub, s.namespace)
			g.Expect(subjects(g, role)).To(ContainElement("system:serviceaccount:" + s.namespace + ":orders-es-es"))
			g.Expect(role.InlinePolicies).To(HaveKey("grant-orders-backup-read-2"))
		}, timeout).Should(Succeed())
		expectNoDriftCorrected(s.namespace)
	})
})

var _ = Describe("drift after a change of the role name prefix", Serial, func() {
	It("records no DriftCorrected for a new role after a change of the prefix", func() {
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-", func(s renameSetup) {
			Expect(k8s.Create(ctx, otherRegistrationWith(s.namespace, "read", readTemplate))).To(Succeed())
			other := newOtherRequest(s.namespace, "orders-backup", "orders")
			grant := grantFor(other, "orders-backup-read", readFragment)
			Expect(k8s.Create(ctx, grant)).To(Succeed())
			expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)
		})

		h := s.startNewHub(withResync(driftResync))

		Eventually(func(g Gomega) {
			_, ok := s.iam.Role(s.oldRole.Name)
			g.Expect(ok).To(BeFalse(), "the old role is gone")
			g.Expect(roleOf(g, h, s.namespace).InlinePolicies).To(HaveKey(grantPolicy))
		}, timeout).Should(Succeed())
		expectNoDriftCorrected(s.namespace)
	})

	It("records no DriftCorrected when a new Grant reaches the old role first", func() {
		var other *pluginsv1alpha1.PluginRequest
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-", func(s renameSetup) {
			Expect(k8s.Create(ctx, otherRegistrationWith(s.namespace, "read", readTemplate))).To(Succeed())
			other = newOtherRequest(s.namespace, "orders-backup", "orders")
			grant := grantFor(other, "orders-backup-read", readFragment)
			Expect(k8s.Create(ctx, grant)).To(Succeed())
			expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)
		})
		// The old role stays in the status while the new hub cannot delete it.
		s.iam.AddRoleToInstanceProfile(s.oldRole.Name, "nodes")
		h, stop := startHub(s.mode, s.registration, "team-b", s.iam, s.eks)
		var current awsfake.Role
		Eventually(func(g Gomega) {
			current = roleOf(g, h, s.namespace)
			g.Expect(current.Tags).To(HaveKey(awskit.TagDesired))
			got := read(g, s.request.DeepCopy())
			g.Expect(got.Status.Outputs).To(HaveKeyWithValue(awskit.OutputPrincipal, current.ARN))
			g.Expect(got.Status.Resources).To(ContainElement(HaveField("Name", s.oldRole.Name)))
		}, timeout).Should(Succeed())
		stop()
		s.iam.RemoveRoleFromInstanceProfile(s.oldRole.Name, "nodes")

		// The outputs name the old role again, as after a status write that
		// did not land, and the first put of the new Grant on the new role
		// fails.
		Eventually(func(g Gomega) {
			got := read(g, s.request.DeepCopy())
			got.Status.Outputs[awskit.OutputPrincipal] = s.oldRole.ARN
			g.Expect(k8s.Status().Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		grant := grantFor(other, "orders-backup-read-2", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		s.iam.FailNextOnRole(current.Name, "PutRolePolicy", errors.New("the policy cannot be put"))
		oldCalls := len(s.iam.RoleCalls(s.oldRole.Name))
		// Controller names are unique in the process.
		s.startNewHub(func(o *awsidentity.Options) { o.Name += "-again" })

		Eventually(func(g Gomega) {
			g.Expect(roleOf(g, h, s.namespace).InlinePolicies).To(HaveKey("grant-orders-backup-read-2"))
			got := read(g, s.request.DeepCopy())
			g.Expect(got.Status.Outputs).To(HaveKeyWithValue(awskit.OutputPrincipal, current.ARN))
		}, timeout).Should(Succeed())
		calls := s.iam.RoleCalls(s.oldRole.Name)[oldCalls:]
		Expect(calls).To(ContainElement("PutRolePolicy"), "the old role got the Grant")
		expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)
		expectNoDriftCorrected(s.namespace)
	})
})
