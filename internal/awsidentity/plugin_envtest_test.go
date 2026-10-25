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
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
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
	role, ok := h.iam.Role(awskit.RoleName("camunda", namespace, "orders"))
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
	ns := newNamespace()
	s := grantSetup{namespace: ns, hub: hubRequest(irsa.registration, ns)}
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
				awskit.TagNamespace: ns,
				awskit.TagClaim:     "orders",
				awskit.TagRequest:   request.Name,
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
			_, ok := irsa.iam.Role(awskit.RoleName("camunda", s.namespace, "orders"))
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
		role, ok := irsa.iam.Role(awskit.RoleName("camunda", s.namespace, "orders"))
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
			RoleName: aws.String(awskit.RoleName("camunda", ns, "orders")),
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
			_, ok := podIdentity.iam.Role(awskit.RoleName("camunda", ns, "orders"))
			g.Expect(ok).To(BeFalse())
		}, timeout).Should(Succeed())
	})
})
