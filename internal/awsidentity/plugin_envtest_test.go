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
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsidentity"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

const timeout = 30 * time.Second

// readTemplate is the grant template of the specs, and readFragment is what
// it renders for the bucket b-1.
const (
	readTemplate = `{"Effect":"Allow","Action":["s3:GetObject"],"Resource":"arn:aws:s3:::{{ .outputs.bucket }}/*"}`
	readFragment = `{"Effect":"Allow","Action":["s3:GetObject"],"Resource":"arn:aws:s3:::b-1/*"}`
)

func read[T client.Object](g Gomega, obj T) T {
	g.Expect(irsa.mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
	return obj
}

func conditionOf(conditions []metav1.Condition, conditionType string) metav1.Condition {
	if c := meta.FindStatusCondition(conditions, conditionType); c != nil {
		return *c
	}
	return metav1.Condition{}
}

// roleOf returns the role of the claim orders in namespace from the fakes
// of h.
func roleOf(g Gomega, h *hub, namespace string) awsfake.Role {
	role, ok := h.iam.Role(h.roleName(namespace))
	g.Expect(ok).To(BeTrue(), "the role of %s/orders exists", namespace)
	return role
}

// subjects returns the sub values of the IRSA trust policy of role.
func subjects(g Gomega, role awsfake.Role) []string {
	var doc struct {
		Statement []struct {
			Condition struct {
				StringEquals map[string]any
			}
		}
	}
	g.Expect(json.Unmarshal([]byte(role.TrustPolicy), &doc)).To(Succeed())
	g.Expect(doc.Statement).To(HaveLen(1))

	values := doc.Statement[0].Condition.StringEquals[issuer+":sub"].([]any)
	subs := make([]string, 0, len(values))
	for _, s := range values {
		subs = append(subs, s.(string))
	}
	return subs
}

// readyRequest creates request and waits until it is Ready.
func readyRequest(request *pluginsv1alpha1.PluginRequest) {
	Expect(k8s.Create(ctx, request)).To(Succeed())
	Eventually(func(g Gomega) {
		got := read(g, request.DeepCopy())
		g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	}, timeout).Should(Succeed())
}

// grantSetup is a ready hub request of the claim orders, and a request and
// a registration of another plugin with the outputs that the grant template
// reads.
type grantSetup struct {
	namespace string
	hub       *pluginsv1alpha1.PluginRequest
	other     *pluginsv1alpha1.PluginRequest
}

func newGrantSetup() grantSetup {
	return newGrantSetupFor(irsa)
}

// newGrantSetupFor is newGrantSetup with a hub request of h.
func newGrantSetupFor(h *hub) grantSetup {
	ns := newNamespace()
	s := grantSetup{namespace: ns, hub: hubRequest(h.registration, ns)}
	readyRequest(s.hub)

	Expect(k8s.Create(ctx, otherRegistrationWith(ns, "read", readTemplate))).To(Succeed())
	s.other = newOtherRequest(ns, "orders-backup", "orders")
	return s
}

// newOtherRequest creates a request of the other plugin with the output
// bucket b-1.
func newOtherRequest(namespace, name, claim string) *pluginsv1alpha1.PluginRequest {
	other := otherRequest(namespace, name, claim, "orders")
	Expect(k8s.Create(ctx, other)).To(Succeed())
	other.Status.Outputs = map[string]string{"bucket": "b-1"}
	Expect(k8s.Status().Update(ctx, other)).To(Succeed())
	return other
}

func grantConditions(g Gomega, grant *pluginsv1alpha1.Grant) []metav1.Condition {
	return read(g, grant.DeepCopy()).Status.Conditions
}

func expectGrant(grant *pluginsv1alpha1.Grant, conditionType string, status metav1.ConditionStatus, reason string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		c := conditionOf(grantConditions(g, grant), conditionType)
		g.Expect(c.Status).To(Equal(status), "%s: %s", conditionType, c.Message)
		g.Expect(c.Reason).To(Equal(reason))
	}, timeout).Should(Succeed())
}

// associationsIn returns the ServiceAccounts of the associations of h in
// namespace.
func associationsIn(h *hub, namespace string) []string {
	var sas []string
	for _, a := range h.eks.Associations() {
		if a.Namespace == namespace {
			sas = append(sas, a.ServiceAccount)
		}
	}
	return sas
}

// statementOfSize returns an allowed statement of n characters.
func statementOfSize(n int) string {
	head := `{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::`
	return head + strings.Repeat("x", n-len(head)-2) + `"}`
}

var _ = Describe("the AWS identity hub", func() {
	It("creates one role for a request and publishes its outputs", func() {
		ns := newNamespace()
		request := hubRequest(irsa.registration, ns)
		readyRequest(request)

		Eventually(func(g Gomega) {
			role := roleOf(g, irsa, ns)
			g.Expect(role.PermissionsBoundary).To(Equal(boundary))
			g.Expect(role.Tags).To(Equal(map[string]string{
				awskit.TagNamespace:  ns,
				awskit.TagClaim:      "orders",
				awskit.TagRequest:    request.Name,
				awskit.TagRequestUID: string(request.UID),
				"cost-center":        "camunda",
			}))

			got := read(g, request.DeepCopy())
			g.Expect(conditionOf(got.Status.Conditions, pluginsv1alpha1.ConditionOutputsReady).Status).
				To(Equal(metav1.ConditionTrue))
			g.Expect(got.Status.Outputs).To(HaveKeyWithValue("principal", role.ARN))
			g.Expect(got.Status.Outputs["serviceAccountAnnotations"]).
				To(MatchJSON(`{"eks.amazonaws.com/role-arn":"` + role.ARN + `"}`))
			g.Expect(got.Status.Summary).NotTo(BeEmpty())
			g.Expect(got.Status.Summary[0]).To(And(HaveField("Label", "Role"), HaveField("Value", role.Name)))
			g.Expect(got.Status.Links).To(HaveLen(1))
			g.Expect(got.Status.Links[0].URL).To(HaveSuffix("/roles/details/" + role.Name))
		}, timeout).Should(Succeed())
	})

	It("refuses a role of its name that another request owns", func() {
		ns := newNamespace()
		name := irsa.roleName(ns)
		owner := awskit.ExtraTags(nil).OwnerTags(ns, "orders", "orders-identity", "uid-of-an-orphaned-request")
		_, _, err := awskit.EnsureRole(ctx, irsa.iam, awskit.RoleSpec{
			Name:        name,
			TrustPolicy: []byte(`{"Version":"2012-10-17","Statement":[]}`),
			Tags:        owner,
		})
		Expect(err).NotTo(HaveOccurred())
		before := len(irsa.iam.RoleCalls(name))

		request := hubRequest(irsa.registration, ns)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			got := read(g, request.DeepCopy())
			for _, conditionType := range []string{pluginsv1alpha1.ConditionOutputsReady, v1.ConditionReady} {
				c := conditionOf(got.Status.Conditions, conditionType)
				g.Expect(c.Status).To(Equal(metav1.ConditionFalse), conditionType)
				g.Expect(c.Reason).To(Equal(awsidentity.ReasonRoleNameInUse), conditionType)
				g.Expect(c.Message).To(ContainSubstring("uid-of-an-orphaned-request"), conditionType)
			}
		}, timeout).Should(Succeed())
		Expect(irsa.iam.RoleCalls(name)[before:]).To(HaveEach("GetRole"))
		Expect(roleOf(Default, irsa, ns).Tags).To(Equal(owner))
	})

	It("trusts the target ServiceAccount and each additional one, and no other", func() {
		ns := newNamespace()
		request := hubRequest(irsa.registration, ns, "orders-es-es", "orders-pg-postgres")
		readyRequest(request)

		Eventually(func(g Gomega) {
			g.Expect(subjects(g, roleOf(g, irsa, ns))).To(ConsistOf(
				"system:serviceaccount:"+ns+":orders-camunda",
				"system:serviceaccount:"+ns+":orders-es-es",
				"system:serviceaccount:"+ns+":orders-pg-postgres",
			))
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			got := read(g, request.DeepCopy())
			got.Spec.Target.AdditionalServiceAccounts = []string{"orders-es-es"}
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(subjects(g, roleOf(g, irsa, ns))).To(ConsistOf(
				"system:serviceaccount:"+ns+":orders-camunda",
				"system:serviceaccount:"+ns+":orders-es-es",
			))
		}, timeout).Should(Succeed())
	})

	It("applies an allowed grant and reports it Ready", func() {
		s := newGrantSetup()
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())

		expectGrant(grant, pluginsv1alpha1.ConditionMatchesTemplate, metav1.ConditionTrue, v1.ReasonHealthy)
		expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)
		expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)
		Eventually(func(g Gomega) {
			role := roleOf(g, irsa, s.namespace)
			g.Expect(role.InlinePolicies).To(HaveKey("grant-orders-backup-read"))
			var doc struct{ Statement []json.RawMessage }
			g.Expect(json.Unmarshal([]byte(role.InlinePolicies["grant-orders-backup-read"]), &doc)).To(Succeed())
			g.Expect(doc.Statement).To(HaveLen(1))
			g.Expect(string(doc.Statement[0])).To(MatchJSON(readFragment))
		}, timeout).Should(Succeed())
	})

	It("refuses a grant over the ceiling with OverCeiling", func() {
		s := newGrantSetup()
		grant := grantFor(s.other, "orders-backup-pass",
			`{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}`)
		Expect(k8s.Create(ctx, grant)).To(Succeed())

		expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionFalse, pluginsv1alpha1.ReasonOverCeiling)
		expectGrant(grant, v1.ConditionReady, metav1.ConditionFalse, pluginsv1alpha1.ReasonOverCeiling)
		Consistently(func(g Gomega) {
			g.Expect(roleOf(g, irsa, s.namespace).InlinePolicies).To(BeEmpty())
		}, 2*time.Second).Should(Succeed())
	})

	It("refuses a grant over the size limit with OverSizeLimit and keeps the applied ones", func() {
		s := newGrantSetup()
		first := grantFor(s.other, "orders-backup-a", statementOfSize(6000))
		Expect(k8s.Create(ctx, first)).To(Succeed())
		expectGrant(first, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)

		second := grantFor(s.other, "orders-backup-b", statementOfSize(5000))
		Expect(k8s.Create(ctx, second)).To(Succeed())

		expectGrant(
			second,
			pluginsv1alpha1.ConditionApplied,
			metav1.ConditionFalse,
			pluginsv1alpha1.ReasonOverSizeLimit,
		)
		expectGrant(second, v1.ConditionReady, metav1.ConditionFalse, pluginsv1alpha1.ReasonOverSizeLimit)
		expectGrant(first, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)
		Eventually(func(g Gomega) {
			policies := roleOf(g, irsa, s.namespace).InlinePolicies
			g.Expect(policies).To(HaveKey("grant-orders-backup-a"))
			g.Expect(policies).NotTo(HaveKey("grant-orders-backup-b"))
		}, timeout).Should(Succeed())
	})

	It("keeps an applied grant when the template changes", func() {
		s := newGrantSetup()
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)
		var before string
		Eventually(func(g Gomega) {
			before = roleOf(g, irsa, s.namespace).InlinePolicies["grant-orders-backup-read"]
			g.Expect(before).NotTo(BeEmpty())
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			reg := read(g, otherRegistrationWith(s.namespace, "", ""))
			reg.Spec.GrantTemplates[0].Template = raw(strings.Replace(readTemplate, "GetObject", "PutObject", 1))
			g.Expect(k8s.Update(ctx, reg)).To(Succeed())
		}, timeout).Should(Succeed())

		expectGrant(grant, pluginsv1alpha1.ConditionMatchesTemplate, metav1.ConditionFalse,
			pluginsv1alpha1.ReasonTemplateChanged)
		expectGrant(grant, v1.ConditionReady, metav1.ConditionFalse, pluginsv1alpha1.ReasonTemplateChanged)
		expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)
		Consistently(func(g Gomega) {
			g.Expect(roleOf(g, irsa, s.namespace).InlinePolicies).
				To(HaveKeyWithValue("grant-orders-backup-read", before))
		}, 2*time.Second).Should(Succeed())
	})

	It("ignores a grant of another claim", func() {
		s := newGrantSetup()
		billing := newOtherRequest(s.namespace, "billing-backup", "billing")
		foreign := grantFor(billing, "billing-backup-read", readFragment)
		Expect(k8s.Create(ctx, foreign)).To(Succeed())

		// The hub reads every Grant of the cluster in one pass, so once a Grant
		// of the claim is Ready, the hub has seen the foreign one.
		own := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, own)).To(Succeed())
		expectGrant(own, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Consistently(func(g Gomega) {
			g.Expect(grantConditions(g, foreign)).To(BeEmpty())
			g.Expect(roleOf(g, irsa, s.namespace).InlinePolicies).
				To(HaveLen(1))
		}, 2*time.Second).Should(Succeed())
	})

	It("puts no statement for a grant that names a request of another registration", func() {
		s := newGrantSetup()
		// A request of the claim that another registration serves.
		stranger := otherRequest(s.namespace, "orders-stranger", "orders", "orders")
		stranger.Spec.Registration = "other-plugin"
		stranger.Labels[pluginsv1alpha1.LabelRegistration] = "other-plugin"
		Expect(k8s.Create(ctx, stranger)).To(Succeed())
		forged := grantFor(s.other, "orders-forged-read", readFragment)
		forged.Labels[pluginsv1alpha1.LabelRequest] = stranger.Name
		Expect(k8s.Create(ctx, forged)).To(Succeed())

		// The hub reads every Grant of the cluster in one pass, so once a later
		// Grant is Ready, the hub has seen the forged one.
		own := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, own)).To(Succeed())
		expectGrant(own, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Expect(roleOf(Default, irsa, s.namespace).InlinePolicies).NotTo(HaveKey("grant-orders-forged-read"))
		Expect(grantConditions(Default, forged)).To(BeEmpty())
	})

	It("keeps the policy of a grant whose cluster label changes", func() {
		s := newGrantSetup()
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Eventually(func(g Gomega) {
			got := read(g, grant.DeepCopy())
			got.Labels[pluginsv1alpha1.LabelCluster] = "billing"
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		// The hub reads every Grant of the cluster in one pass, so once a later
		// Grant is Ready, the hub has seen the label change.
		later := grantFor(s.other, "orders-later-read", readFragment)
		Expect(k8s.Create(ctx, later)).To(Succeed())
		expectGrant(later, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Expect(roleOf(Default, irsa, s.namespace).InlinePolicies).To(HaveKey("grant-orders-backup-read"))
	})

	It("keeps the policy of a grant whose request label moves to another claim", func() {
		s := newGrantSetup()
		billing := newOtherRequest(s.namespace, "billing-backup", "billing")
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Eventually(func(g Gomega) {
			got := read(g, grant.DeepCopy())
			got.Labels[pluginsv1alpha1.LabelRequest] = billing.Name
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		// The hub reads every Grant of the cluster in one pass, so once a later
		// Grant is Ready, the hub has seen the label change.
		later := grantFor(s.other, "orders-later-read", readFragment)
		Expect(k8s.Create(ctx, later)).To(Succeed())
		expectGrant(later, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Expect(roleOf(Default, irsa, s.namespace).InlinePolicies).To(HaveKey("grant-orders-backup-read"))
	})

	It("keeps the policy of a grant whose request is gone", func() {
		s := newGrantSetup()
		kept := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, kept)).To(Succeed())
		expectGrant(kept, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Expect(k8s.Delete(ctx, s.other)).To(Succeed())
		// The hub reads every Grant of the cluster in one pass, so once a later
		// Grant is Ready, the hub has seen the request gone.
		later := newOtherRequest(s.namespace, "orders-later", "orders")
		laterGrant := grantFor(later, "orders-later-read", readFragment)
		Expect(k8s.Create(ctx, laterGrant)).To(Succeed())
		expectGrant(laterGrant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Expect(roleOf(Default, irsa, s.namespace).InlinePolicies).To(HaveKey("grant-orders-backup-read"))
	})

	It("removes the policy of a deleted grant while the request names no ServiceAccount", func() {
		s := newGrantSetup()
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)
		Eventually(func(g Gomega) {
			got := read(g, s.hub.DeepCopy())
			got.Spec.Target.ServiceAccountName = ""
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		Eventually(func(g Gomega) {
			got := read(g, s.hub.DeepCopy())
			g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Reason).To(Equal("MissingServiceAccount"))
		}, timeout).Should(Succeed())

		Expect(k8s.Delete(ctx, grant)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(roleOf(g, irsa, s.namespace).InlinePolicies).To(BeEmpty())
		}, timeout).Should(Succeed())
	})

	It("revokes the trust of a request that names no ServiceAccount any more", func() {
		ns := newNamespace()
		request := hubRequest(irsa.registration, ns, "orders-es-es")
		readyRequest(request)

		Eventually(func(g Gomega) {
			got := read(g, request.DeepCopy())
			got.Spec.Target.ServiceAccountName = ""
			got.Spec.Target.AdditionalServiceAccounts = nil
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(roleOf(g, irsa, ns).TrustPolicy).To(MatchJSON(`{"Version":"2012-10-17","Statement":[{
				"Effect":"Deny","Principal":{"Federated":"` + providerARN + `"},
				"Action":"sts:AssumeRoleWithWebIdentity"}]}`))
			got := read(g, request.DeepCopy())
			g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Reason).To(Equal("MissingServiceAccount"))
			g.Expect(got.Status.Resources).To(ContainElement(And(
				HaveField("Kind", "IAMRole"),
				HaveField("Name", irsa.roleName(ns)),
			)))
		}, timeout).Should(Succeed())
	})

	It("removes the policy of a deleted grant", func() {
		s := newGrantSetup()
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)

		Expect(k8s.Delete(ctx, grant)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(roleOf(g, irsa, s.namespace).InlinePolicies).To(BeEmpty())
		}, timeout).Should(Succeed())
	})

	It("deletes the role and its policies with the deletion policy Delete", func() {
		s := newGrantSetup()
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)

		Expect(k8s.Delete(ctx, s.hub)).To(Succeed())

		Eventually(func(g Gomega) {
			err := irsa.mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(s.hub), s.hub.DeepCopy())
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			_, ok := irsa.iam.Role(irsa.roleName(s.namespace))
			g.Expect(ok).To(BeFalse())
		}, timeout).Should(Succeed())
	})

	It("keeps the role and its policies with the deletion policy Orphan", func() {
		s := newGrantSetup()
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)
		Eventually(func(g Gomega) {
			got := read(g, s.hub.DeepCopy())
			got.Spec.DeletionPolicy = pluginsv1alpha1.DeletionPolicyOrphan
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())

		Expect(k8s.Delete(ctx, s.hub)).To(Succeed())

		Eventually(func(g Gomega) {
			err := irsa.mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(s.hub), s.hub.DeepCopy())
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, timeout).Should(Succeed())
		role, ok := irsa.iam.Role(irsa.roleName(s.namespace))
		Expect(ok).To(BeTrue())
		Expect(role.InlinePolicies).To(HaveKey("grant-orders-backup-read"))
	})

	It("deletes the associations of a request whose role was deleted by hand", func() {
		ns := newNamespace()
		request := hubRequest(podIdentity.registration, ns)
		readyRequest(request)
		Eventually(func(g Gomega) {
			g.Expect(read(g, request.DeepCopy()).Status.Outputs).To(HaveKey("principal"))
		}, timeout).Should(Succeed())

		_, err := podIdentity.iam.DeleteRole(ctx, &iam.DeleteRoleInput{
			RoleName: aws.String(podIdentity.roleName(ns)),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			err := irsa.mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), request.DeepCopy())
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, timeout).Should(Succeed())
		for _, a := range podIdentity.eks.Associations() {
			Expect(a.Namespace).NotTo(Equal(ns))
		}
	})

	It(
		"deletes the associations of a request whose role was deleted by hand and that lost its ServiceAccounts",
		func() {
			ns := newNamespace()
			request := hubRequest(podIdentity.registration, ns)
			readyRequest(request)
			Eventually(func(g Gomega) {
				g.Expect(read(g, request.DeepCopy()).Status.Outputs).To(HaveKey("principal"))
			}, timeout).Should(Succeed())

			_, err := podIdentity.iam.DeleteRole(ctx, &iam.DeleteRoleInput{
				RoleName: aws.String(podIdentity.roleName(ns)),
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				got := read(g, request.DeepCopy())
				got.Spec.Target.ServiceAccountName = ""
				got.Spec.Target.AdditionalServiceAccounts = nil
				g.Expect(k8s.Update(ctx, got)).To(Succeed())
			}, timeout).Should(Succeed())

			Eventually(func(g Gomega) {
				for _, a := range podIdentity.eks.Associations() {
					g.Expect(a.Namespace).NotTo(Equal(ns))
				}
				got := read(g, request.DeepCopy())
				g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Reason).
					To(Equal("MissingServiceAccount"))
			}, timeout).Should(Succeed())
		},
	)

	It("removes the policy of a deleted grant while an association fails", func() {
		s := newGrantSetupFor(podIdentity)
		grant := grantFor(s.other, "orders-backup-read", readFragment)
		Expect(k8s.Create(ctx, grant)).To(Succeed())
		expectGrant(grant, pluginsv1alpha1.ConditionApplied, metav1.ConditionTrue, v1.ReasonHealthy)

		// Another role holds the ServiceAccount taken, so each association of
		// the request for it fails.
		_, err := podIdentity.eks.CreatePodIdentityAssociation(ctx, &eks.CreatePodIdentityAssociationInput{
			ClusterName:    aws.String(eksCluster),
			Namespace:      aws.String(s.namespace),
			ServiceAccount: aws.String("taken"),
			RoleArn:        aws.String("arn:aws:iam::111122223333:role/other"),
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			got := read(g, s.hub.DeepCopy())
			got.Spec.Target.AdditionalServiceAccounts = []string{"taken"}
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())
		Eventually(func(g Gomega) {
			ready := conditionOf(read(g, s.hub.DeepCopy()).Status.Conditions, v1.ConditionReady)
			g.Expect(ready.Message).To(ContainSubstring("belongs to another request"))
		}, timeout).Should(Succeed())

		Expect(k8s.Delete(ctx, grant)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(roleOf(g, podIdentity, s.namespace).InlinePolicies).To(BeEmpty())
		}, timeout).Should(Succeed())
	})

	It("deletes the association of a ServiceAccount that leaves the request", func() {
		ns := newNamespace()
		request := hubRequest(podIdentity.registration, ns, "orders-es-es")
		readyRequest(request)
		Eventually(func(g Gomega) {
			g.Expect(associationsIn(podIdentity, ns)).To(ConsistOf("orders-camunda", "orders-es-es"))
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			got := read(g, request.DeepCopy())
			got.Spec.Target.AdditionalServiceAccounts = nil
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(associationsIn(podIdentity, ns)).To(ConsistOf("orders-camunda"))
		}, timeout).Should(Succeed())
	})

	It("keeps one Pod Identity association for each ServiceAccount", func() {
		ns := newNamespace()
		request := hubRequest(podIdentity.registration, ns, "orders-es-es")
		readyRequest(request)

		Eventually(func(g Gomega) {
			role := roleOf(g, podIdentity, ns)
			g.Expect(role.TrustPolicy).To(ContainSubstring("pods.eks.amazonaws.com"))
			got := read(g, request.DeepCopy())
			g.Expect(got.Status.Outputs["serviceAccountAnnotations"]).To(MatchJSON(`{}`))

			var sas []string
			for _, a := range podIdentity.eks.Associations() {
				if a.Namespace == ns {
					g.Expect(a.RoleARN).To(Equal(role.ARN))
					g.Expect(a.Cluster).To(Equal(eksCluster))
					sas = append(sas, a.ServiceAccount)
				}
			}
			g.Expect(sas).To(ConsistOf("orders-camunda", "orders-es-es"))
		}, timeout).Should(Succeed())

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			for _, a := range podIdentity.eks.Associations() {
				g.Expect(a.Namespace).NotTo(Equal(ns))
			}
			_, ok := podIdentity.iam.Role(podIdentity.roleName(ns))
			g.Expect(ok).To(BeFalse())
		}, timeout).Should(Succeed())
	})
})

// renameSetup is a request that was Ready on a hub with the prefix camunda,
// and that hub stopped, before a hub with another prefix takes over its
// registration and its fakes.
type renameSetup struct {
	mode         awskit.Mode
	registration string
	namespace    string
	iam          *awsfake.IAM
	eks          *awsfake.EKS
	request      *pluginsv1alpha1.PluginRequest
	// oldRole is the role of the request on the stopped hub.
	oldRole awsfake.Role
}

// newRenameSetup makes s. Each of before runs while the hub with the prefix
// camunda serves the Ready request.
func newRenameSetup(mode awskit.Mode, registrationPrefix string, before ...func(renameSetup)) renameSetup {
	ns := newNamespace()
	s := renameSetup{
		mode:         mode,
		registration: registrationPrefix + ns,
		namespace:    ns,
		iam:          awsfake.NewIAM(),
		eks:          awsfake.NewEKS(),
	}

	old, stop := startHub(mode, s.registration, "camunda", s.iam, s.eks)
	s.request = hubRequest(s.registration, ns)
	readyRequest(s.request)
	for _, f := range before {
		f(s)
	}
	stop()

	s.oldRole = roleOf(Default, old, ns)
	return s
}

// update changes the request of s with change while no hub runs.
func (s renameSetup) update(change func(*pluginsv1alpha1.PluginRequest)) {
	Eventually(func(g Gomega) {
		got := read(g, s.request.DeepCopy())
		change(got)
		g.Expect(k8s.Update(ctx, got)).To(Succeed())
	}, timeout).Should(Succeed())
}

// startNewHub starts the hub with the prefix team-b on the fakes of s, and
// stops it at the end of the spec.
func (s renameSetup) startNewHub(edits ...func(*awsidentity.Options)) *hub {
	h, stop := startHub(s.mode, s.registration, "team-b", s.iam, s.eks, edits...)
	DeferCleanup(stop)
	return h
}

// putRole puts a role of name into the fakes of s, owned by the request
// UID uid.
func (s renameSetup) putRole(name, uid string) {
	_, _, err := awskit.EnsureRole(ctx, s.iam, awskit.RoleSpec{
		Name:        name,
		TrustPolicy: []byte(`{"Version":"2012-10-17","Statement":[]}`),
		Tags:        awskit.ExtraTags(nil).OwnerTags(s.namespace, "orders", s.request.Name, uid),
	})
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("a Grant sync that fails", Serial, func() {
	It("still deletes the association of a ServiceAccount that left the request", func() {
		ns := newNamespace()
		iamFake, eksFake := awsfake.NewIAM(), awsfake.NewEKS()
		h, stop := startHub(awskit.ModePodIdentity, "aws-pod-identity-"+ns, "camunda", iamFake, eksFake)
		DeferCleanup(stop)
		request := hubRequest(h.registration, ns, "orders-es-es")
		readyRequest(request)
		Expect(associationsIn(h, ns)).To(ConsistOf("orders-camunda", "orders-es-es"))

		for range 1000 {
			iamFake.FailNext("ListRolePolicies", errors.New("the policies cannot be listed"))
		}
		Eventually(func(g Gomega) {
			got := read(g, request.DeepCopy())
			got.Spec.Target.AdditionalServiceAccounts = nil
			g.Expect(k8s.Update(ctx, got)).To(Succeed())
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(associationsIn(h, ns)).To(ConsistOf("orders-camunda"))
		}, timeout).Should(Succeed())
	})
})

var _ = Describe("a change of the role name prefix", Serial, func() {
	DescribeTable("deletes the old role after the outputs name the new one",
		func(mode awskit.Mode, registrationPrefix string) {
			s := newRenameSetup(mode, registrationPrefix)
			h := s.startNewHub()

			Eventually(func(g Gomega) {
				_, ok := s.iam.Role(s.oldRole.Name)
				g.Expect(ok).To(BeFalse(), "the old role is gone")
				role := roleOf(g, h, s.namespace)
				got := read(g, s.request.DeepCopy())
				g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
				g.Expect(got.Status.Outputs).To(HaveKeyWithValue("principal", role.ARN))
				g.Expect(got.Status.Resources).NotTo(ContainElement(HaveField("Name", s.oldRole.Name)))
				g.Expect(eventNotes(g, s.namespace, "RoleDeleted")).To(ConsistOf(
					And(ContainSubstring(s.oldRole.Name), ContainSubstring(role.Name)),
				))
			}, timeout).Should(Succeed())

			if mode == awskit.ModePodIdentity {
				role := roleOf(Default, h, s.namespace)
				Expect(s.eks.Associations()).To(HaveEach(HaveField("RoleARN", role.ARN)))
				Expect(s.eks.Calls()).To(ContainElement("UpdatePodIdentityAssociation"))
			}
		},
		Entry("IRSA", awskit.ModeIRSA, "aws-irsa-"),
		Entry("Pod Identity", awskit.ModePodIdentity, "aws-pod-identity-"),
	)

	It("deletes both roles when the request is deleted after a change of the prefix", func() {
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-")
		newName := awskit.RoleName("team-b", issuer, s.namespace, "orders")
		s.putRole(newName, string(s.request.UID))
		// The finalizer holds the request until a hub runs Delete.
		Expect(k8s.Delete(ctx, s.request)).To(Succeed())

		s.startNewHub()

		Eventually(func(g Gomega) {
			err := k8s.Get(ctx, client.ObjectKeyFromObject(s.request), s.request.DeepCopy())
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the request is gone")
			g.Expect(s.iam.Roles()).To(BeEmpty())
			g.Expect(eventCount(g, s.namespace, "RoleDeleted")).To(Equal(2))
		}, timeout).Should(Succeed())
	})

	It("keeps an old role whose UID tag names another request", func() {
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-")
		_, err := s.iam.TagRole(ctx, &iam.TagRoleInput{
			RoleName: aws.String(s.oldRole.Name),
			Tags:     []iamtypes.Tag{{Key: aws.String(awskit.TagRequestUID), Value: aws.String("another-uid")}},
		})
		Expect(err).NotTo(HaveOccurred())

		h := s.startNewHub()

		Eventually(func(g Gomega) {
			role := roleOf(g, h, s.namespace)
			got := read(g, s.request.DeepCopy())
			g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
			g.Expect(got.Status.Outputs).To(HaveKeyWithValue("principal", role.ARN))
			g.Expect(got.Status.Resources).NotTo(ContainElement(HaveField("Name", s.oldRole.Name)))
			g.Expect(eventNotes(g, s.namespace, "RoleKept")).To(ConsistOf(
				And(ContainSubstring(s.oldRole.Name), ContainSubstring("another-uid")),
			))
		}, timeout).Should(Succeed())
		_, ok := s.iam.Role(s.oldRole.Name)
		Expect(ok).To(BeTrue(), "the old role stays")
		Expect(s.iam.RoleCalls(s.oldRole.Name)).NotTo(ContainElement("DeleteRole"))
	})

	It("keeps the old role while its new name is in use", func() {
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-")
		s.putRole(awskit.RoleName("team-b", issuer, s.namespace, "orders"), "another-uid")

		s.startNewHub()

		Eventually(func(g Gomega) {
			got := read(g, s.request.DeepCopy())
			c := conditionOf(got.Status.Conditions, v1.ConditionReady)
			g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(c.Reason).To(Equal(awsidentity.ReasonRoleNameInUse))
			g.Expect(got.Status.Resources).To(ContainElement(HaveField("Name", s.oldRole.Name)))
			g.Expect(got.Status.Outputs).To(HaveKeyWithValue("principal", s.oldRole.ARN))
		}, timeout).Should(Succeed())
		_, ok := s.iam.Role(s.oldRole.Name)
		Expect(ok).To(BeTrue(), "the old role stays")
	})

	It("deletes an old role that an older version of the plugin made", func() {
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-")
		// An older version tagged its roles without the UID of the request.
		_, err := awskit.DeleteRole(ctx, s.iam, s.oldRole.Name, string(s.request.UID))
		Expect(err).NotTo(HaveOccurred())
		_, _, err = awskit.EnsureRole(ctx, s.iam, awskit.RoleSpec{
			Name:        s.oldRole.Name,
			TrustPolicy: []byte(s.oldRole.TrustPolicy),
			Tags:        awskit.RequestTags(s.namespace, "orders", s.request.Name),
		})
		Expect(err).NotTo(HaveOccurred())

		h := s.startNewHub()

		Eventually(func(g Gomega) {
			_, ok := s.iam.Role(s.oldRole.Name)
			g.Expect(ok).To(BeFalse(), "the old role is gone")
			role := roleOf(g, h, s.namespace)
			g.Expect(eventNotes(g, s.namespace, "RoleDeleted")).To(ConsistOf(
				And(ContainSubstring(s.oldRole.Name), ContainSubstring(role.Name)),
			))
		}, timeout).Should(Succeed())
	})

	It("removes the policy of a deleted grant from the old role while the new name is in use", func() {
		var grant *pluginsv1alpha1.Grant
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-", func(s renameSetup) {
			Expect(k8s.Create(ctx, otherRegistrationWith(s.namespace, "read", readTemplate))).To(Succeed())
			other := newOtherRequest(s.namespace, "orders-backup", "orders")
			grant = grantFor(other, "orders-backup-read", readFragment)
			Expect(k8s.Create(ctx, grant)).To(Succeed())
			expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)
		})
		Expect(s.oldRole.InlinePolicies).To(HaveKey("grant-orders-backup-read"))
		s.putRole(awskit.RoleName("team-b", issuer, s.namespace, "orders"), "another-uid")
		Expect(k8s.Delete(ctx, grant)).To(Succeed())

		s.startNewHub()

		Eventually(func(g Gomega) {
			old, ok := s.iam.Role(s.oldRole.Name)
			g.Expect(ok).To(BeTrue(), "the old role stays")
			g.Expect(old.InlinePolicies).To(BeEmpty())
			got := read(g, s.request.DeepCopy())
			g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Reason).
				To(Equal(awsidentity.ReasonRoleNameInUse))
		}, timeout).Should(Succeed())
	})

	It("revokes the trust of the old role when the request names no ServiceAccount before the new role exists", func() {
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-")
		s.update(func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Target.ServiceAccountName = "" })

		h := s.startNewHub()

		Eventually(func(g Gomega) {
			got := read(g, s.request.DeepCopy())
			g.Expect(conditionOf(got.Status.Conditions, pluginsv1alpha1.ConditionOutputsReady).Reason).
				To(Equal(awsidentity.ReasonMissingServiceAccount))
			old, ok := s.iam.Role(s.oldRole.Name)
			g.Expect(ok).To(BeTrue(), "the old role stays")
			g.Expect(old.TrustPolicy).To(ContainSubstring(`"Effect":"Deny"`))
			g.Expect(old.TrustPolicy).NotTo(ContainSubstring("orders-camunda"))
			g.Expect(got.Status.Resources).To(ContainElement(HaveField("Name", s.oldRole.Name)))
		}, timeout).Should(Succeed())
		_, ok := s.iam.Role(h.roleName(s.namespace))
		Expect(ok).To(BeFalse(), "no role of the new name")
	})

	It(
		"revokes the trust of both roles when the request names no ServiceAccount during a change of the prefix",
		func() {
			s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-")
			newName := awskit.RoleName("team-b", issuer, s.namespace, "orders")
			s.putRole(newName, string(s.request.UID))
			s.update(func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Target.ServiceAccountName = "" })

			s.startNewHub()

			Eventually(func(g Gomega) {
				for _, name := range []string{s.oldRole.Name, newName} {
					role, ok := s.iam.Role(name)
					g.Expect(ok).To(BeTrue(), "the role %s stays", name)
					g.Expect(role.TrustPolicy).To(ContainSubstring(`"Effect":"Deny"`), name)
				}
			}, timeout).Should(Succeed())
		},
	)

	It("updates the trust of the old role while the new name is in use", func() {
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-")
		s.putRole(awskit.RoleName("team-b", issuer, s.namespace, "orders"), "another-uid")
		s.update(func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Target.AdditionalServiceAccounts = []string{"orders-es-es"}
		})

		s.startNewHub()

		Eventually(func(g Gomega) {
			old, ok := s.iam.Role(s.oldRole.Name)
			g.Expect(ok).To(BeTrue(), "the old role stays")
			g.Expect(subjects(g, old)).To(ConsistOf(
				"system:serviceaccount:"+s.namespace+":orders-camunda",
				"system:serviceaccount:"+s.namespace+":orders-es-es",
			))
			got := read(g, s.request.DeepCopy())
			g.Expect(conditionOf(got.Status.Conditions, v1.ConditionReady).Reason).
				To(Equal(awsidentity.ReasonRoleNameInUse))
		}, timeout).Should(Succeed())
	})

	It("removes the policy of a deleted grant from the old role while an association fails to move", func() {
		var grant *pluginsv1alpha1.Grant
		s := newRenameSetup(awskit.ModePodIdentity, "aws-pod-identity-", func(s renameSetup) {
			Expect(k8s.Create(ctx, otherRegistrationWith(s.namespace, "read", readTemplate))).To(Succeed())
			other := newOtherRequest(s.namespace, "orders-backup", "orders")
			grant = grantFor(other, "orders-backup-read", readFragment)
			Expect(k8s.Create(ctx, grant)).To(Succeed())
			expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)
		})
		Expect(s.oldRole.InlinePolicies).To(HaveKey("grant-orders-backup-read"))
		for range 1000 {
			s.eks.FailNext("UpdatePodIdentityAssociation", errors.New("the association does not move"))
		}
		Expect(k8s.Delete(ctx, grant)).To(Succeed())

		s.startNewHub()

		Eventually(func(g Gomega) {
			old, ok := s.iam.Role(s.oldRole.Name)
			g.Expect(ok).To(BeTrue(), "the old role stays")
			g.Expect(old.InlinePolicies).To(BeEmpty())
		}, timeout).Should(Succeed())
	})

	It("says that an instance profile uses a role that it cannot delete", func() {
		s := newRenameSetup(awskit.ModeIRSA, "aws-irsa-")
		s.iam.AddRoleToInstanceProfile(s.oldRole.Name, "nodes")
		Expect(k8s.Delete(ctx, s.request)).To(Succeed())
		DeferCleanup(func() {
			// The fake keeps the role in the instance profile, so the spec
			// releases the request itself.
			Eventually(func(g Gomega) {
				got := read(g, s.request.DeepCopy())
				got.Finalizers = nil
				g.Expect(k8s.Update(ctx, got)).To(Succeed())
			}, timeout).Should(Succeed())
		})

		s.startNewHub()

		Eventually(func(g Gomega) {
			got := read(g, s.request.DeepCopy())
			c := conditionOf(got.Status.Conditions, v1.ConditionReady)
			g.Expect(c.Reason).To(Equal(pluginkit.ReasonDeleteFailed))
			g.Expect(c.Message).To(ContainSubstring(s.oldRole.Name))
			g.Expect(c.Message).To(ContainSubstring("an instance profile uses it"))
		}, timeout).Should(Succeed())
	})

	It("puts the Grants on the new role before it moves an association to it", func() {
		s := newRenameSetup(awskit.ModePodIdentity, "aws-pod-identity-", func(s renameSetup) {
			Expect(k8s.Create(ctx, otherRegistrationWith(s.namespace, "read", readTemplate))).To(Succeed())
			other := newOtherRequest(s.namespace, "orders-backup", "orders")
			grant := grantFor(other, "orders-backup-read", readFragment)
			Expect(k8s.Create(ctx, grant)).To(Succeed())
			expectGrant(grant, v1.ConditionReady, metav1.ConditionTrue, v1.ReasonHealthy)
		})
		moves := &policiesAtMove{EKS: s.eks, iam: s.iam}

		s.startNewHub(func(o *awsidentity.Options) { o.Clients.EKS = moves })

		Eventually(func(g Gomega) {
			_, ok := s.iam.Role(s.oldRole.Name)
			g.Expect(ok).To(BeFalse(), "the old role is gone")
		}, timeout).Should(Succeed())
		Expect(moves.seen()).NotTo(BeEmpty())
		for _, policies := range moves.seen() {
			Expect(
				policies,
			).To(HaveKey("grant-orders-backup-read"), "the role had its Grants when the association moved")
		}
	})
})

// policiesAtMove records the inline policies of the target role of each
// UpdatePodIdentityAssociation call.
type policiesAtMove struct {
	*awsfake.EKS
	iam *awsfake.IAM

	mu       sync.Mutex
	policies []map[string]string
}

func (m *policiesAtMove) UpdatePodIdentityAssociation(
	ctx context.Context,
	in *eks.UpdatePodIdentityAssociationInput,
	opts ...func(*eks.Options),
) (*eks.UpdatePodIdentityAssociationOutput, error) {
	arn := aws.ToString(in.RoleArn)
	role, _ := m.iam.Role(arn[strings.LastIndex(arn, "/")+1:])
	m.mu.Lock()
	m.policies = append(m.policies, role.InlinePolicies)
	m.mu.Unlock()
	return m.EKS.UpdatePodIdentityAssociation(ctx, in, opts...)
}

func (m *policiesAtMove) seen() []map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.policies)
}
