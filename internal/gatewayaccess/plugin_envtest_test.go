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

package gatewayaccess_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/gatewayaccess"
)

const timeout = 30 * time.Second

var namespaceSeq atomic.Int32

// newNamespace creates a namespace of its own for one spec.
func newNamespace() string {
	name := fmt.Sprintf("team-%d", namespaceSeq.Add(1))
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func clusterTarget(name string) pluginsv1alpha1.TargetRef {
	return pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: name, ServiceAccountName: name + "-camunda"}
}

func readRequest(g Gomega, request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.PluginRequest {
	var read pluginsv1alpha1.PluginRequest
	g.Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), &read)).To(Succeed())
	return &read
}

func condition(request *pluginsv1alpha1.PluginRequest, conditionType string) metav1.Condition {
	if c := meta.FindStatusCondition(request.Status.Conditions, conditionType); c != nil {
		return *c
	}
	return metav1.Condition{}
}

func summaryValue(request *pluginsv1alpha1.PluginRequest, label string) pluginsv1alpha1.SummaryItem {
	for _, item := range request.Status.Summary {
		if item.Label == label {
			return item
		}
	}
	return pluginsv1alpha1.SummaryItem{}
}

// routesOf lists the routes with the request label of request.
func routesOf(g Gomega, request *pluginsv1alpha1.PluginRequest) ([]gatewayv1.HTTPRoute, []gatewayv1.GRPCRoute) {
	opts := []client.ListOption{
		client.InNamespace(request.Namespace),
		client.MatchingLabels{pluginsv1alpha1.LabelRequest: request.Name},
	}
	var http gatewayv1.HTTPRouteList
	g.Expect(mgr.GetAPIReader().List(ctx, &http, opts...)).To(Succeed())
	var grpc gatewayv1.GRPCRouteList
	g.Expect(mgr.GetAPIReader().List(ctx, &grpc, opts...)).To(Succeed())
	return http.Items, grpc.Items
}

// readyClusterRequest creates the cluster orders with its endpoints and a
// request for it, and waits until the request is Ready.
func readyClusterRequest(namespace string) *pluginsv1alpha1.PluginRequest {
	cluster := newCluster(namespace, "orders")
	Expect(k8s.Create(ctx, cluster)).To(Succeed())
	Expect(publishEndpoints(ctx, k8s, cluster, standaloneEndpoints("orders"))).To(Succeed())

	request := accessRequest(namespace, "orders-access", clusterTarget("orders"), gatewayParams())
	Expect(k8s.Create(ctx, request)).To(Succeed())

	Eventually(func(g Gomega) {
		g.Expect(condition(readRequest(g, request), v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	}, timeout).Should(Succeed())
	return request
}

// ordersCluster creates the cluster orders with its endpoints.
func ordersCluster(namespace string) {
	GinkgoHelper()
	cluster := newCluster(namespace, "orders")
	Expect(k8s.Create(ctx, cluster)).To(Succeed())
	Expect(publishEndpoints(ctx, k8s, cluster, standaloneEndpoints("orders"))).To(Succeed())
}

// policyParams are gatewayParams with templates as the policy templates.
func policyParams(templates ...gatewayaccess.PolicyTemplate) gatewayaccess.Params {
	p := gatewayParams()
	p.PolicyTemplates = templates
	return p
}

// policyByName returns the policy name of policies, or fails.
func policyByName(policies []unstructured.Unstructured, name string) unstructured.Unstructured {
	GinkgoHelper()
	for _, p := range policies {
		if p.GetName() == name {
			return p
		}
	}
	Fail("no policy " + name)
	return unstructured.Unstructured{}
}

// policyExists reports whether the ExamplePolicy name exists in namespace.
func policyExists(g Gomega, namespace, name string) bool {
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(examplePolicyKind)
	err := mgr.GetAPIReader().Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, policy)
	if apierrors.IsNotFound(err) {
		return false
	}
	g.Expect(err).NotTo(HaveOccurred())
	return true
}

// setCertificate writes the data of secret to the certificate Secret of the
// suite.
func setCertificate(secret *corev1.Secret) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		var current corev1.Secret
		g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(secret), &current)).To(Succeed())
		current.Data = secret.Data
		g.Expect(k8s.Update(ctx, &current)).To(Succeed())
	}, timeout).Should(Succeed())
}

var _ = Describe("the gateway-access plugin", func() {
	It("publishes the outputs before the target exists", func() {
		ns := newNamespace()
		request := accessRequest(ns, "orders-access", clusterTarget("orders"), gatewayParams())
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			read := readRequest(g, request)
			g.Expect(condition(read, pluginsv1alpha1.ConditionOutputsReady).Status).To(Equal(metav1.ConditionTrue))
			g.Expect(read.Status.Outputs).To(Equal(map[string]string{
				"externalUrl":    "https://orders." + ns + ".example.com",
				"grpcHost":       "grpc-orders." + ns + ".example.com",
				"optimizeUrl":    "https://optimize-orders." + ns + ".example.com",
				"managementHost": "management-orders." + ns + ".example.com",
			}))
			ready := condition(read, v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(pluginsv1alpha1.ReasonWaitingForEndpoints))
		}, timeout).Should(Succeed())

		Consistently(func(g Gomega) {
			http, grpc := routesOf(g, request)
			g.Expect(http).To(BeEmpty())
			g.Expect(grpc).To(BeEmpty())
		}, 2*time.Second).Should(Succeed())
	})

	It("routes the cluster once it publishes endpoints, and is Ready when the Gateway accepts the routes", func() {
		ns := newNamespace()
		request := readyClusterRequest(ns)
		read := readRequest(Default, request)

		Expect(condition(read, v1.ConditionReady).Reason).To(Equal(v1.ReasonHealthy))
		Expect(summaryValue(read, "Host").Value).To(Equal("orders." + ns + ".example.com"))
		Expect(summaryValue(read, "Routes accepted").Value).To(Equal("2 of 2"))
		Expect(summaryValue(read, "Certificate expires").Severity).To(Equal(pluginsv1alpha1.SeverityWarning))
		Expect(read.Status.Resources).To(HaveLen(2))
		for _, r := range read.Status.Resources {
			Expect(r.OnOrphan).To(Equal(pluginsv1alpha1.OnOrphanDeleted))
		}

		http, grpc := routesOf(Default, request)
		Expect(http).To(HaveLen(1))
		Expect(grpc).To(HaveLen(1))
		Expect(http[0].Name).To(Equal("orders-access"))
		Expect(http[0].Spec.Hostnames).To(ConsistOf(gatewayv1.Hostname("orders." + ns + ".example.com")))
		Expect(http[0].Spec.Rules).To(HaveLen(6))
		Expect(grpc[0].Name).To(Equal("orders-access-grpc"))
		for _, obj := range []client.Object{&http[0], &grpc[0]} {
			Expect(obj.GetLabels()).To(HaveKeyWithValue(pluginsv1alpha1.LabelRequest, "orders-access"))
			Expect(obj.GetLabels()).To(HaveKeyWithValue(pluginsv1alpha1.LabelRegistration, "gateway-public"))
			Expect(obj.GetOwnerReferences()).To(ContainElement(HaveField("UID", read.UID)))
		}
	})

	It("reads a renewed certificate again after the resync", func() {
		ns := newNamespace()
		request := readyClusterRequest(ns)
		Expect(summaryValue(readRequest(Default, request), "Certificate expires").Severity).
			To(Equal(pluginsv1alpha1.SeverityWarning))

		// The plugin does not watch the certificate Secret, so after the
		// reconciles of the routes settle, only the resync reads the renewed
		// certificate.
		Eventually(func(g Gomega) {
			before := readRequest(g, request).ResourceVersion
			time.Sleep(3 * time.Second)
			g.Expect(readRequest(g, request).ResourceVersion).To(Equal(before))
		}, timeout).Should(Succeed())
		renewed, err := certificateSecret(time.Now().Add(60 * 24 * time.Hour))
		Expect(err).NotTo(HaveOccurred())
		setCertificate(renewed)
		DeferCleanup(func() {
			expiring, err := certificateSecret(time.Now().Add(10 * 24 * time.Hour))
			Expect(err).NotTo(HaveOccurred())
			setCertificate(expiring)
		})

		Eventually(func(g Gomega) {
			item := summaryValue(readRequest(g, request), "Certificate expires")
			g.Expect(item.Severity).To(Equal(pluginsv1alpha1.SeverityInfo))
		}, timeout).Should(Succeed())
	})

	It("reports the reason of a refused route", func() {
		ns := newNamespace()
		refuse.Store("refused-access", true)
		DeferCleanup(func() { refuse.Delete("refused-access") })

		cluster := newCluster(ns, "orders")
		Expect(k8s.Create(ctx, cluster)).To(Succeed())
		Expect(publishEndpoints(ctx, k8s, cluster, standaloneEndpoints("orders"))).To(Succeed())
		request := accessRequest(ns, "refused-access", clusterTarget("orders"), gatewayParams())
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(readRequest(g, request), v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal("NotAllowedByListeners"))
			g.Expect(ready.Message).To(ContainSubstring("HTTPRoute refused-access"))
		}, timeout).Should(Succeed())
	})

	It("routes Optimize, and deletes the Optimize route when the Optimize is gone", func() {
		ns := newNamespace()
		request := readyClusterRequest(ns)

		optimize := newOptimize(ns, "analytics", "orders")
		Expect(k8s.Create(ctx, optimize)).To(Succeed())
		Expect(publishEndpoints(ctx, k8s, optimize, []v1.Endpoint{
			httpEndpoint("optimize", "analytics-optimize-webapp", 8090, "/"),
		})).To(Succeed())

		Eventually(func(g Gomega) {
			var route gatewayv1.HTTPRoute
			key := client.ObjectKey{Namespace: ns, Name: "orders-access-optimize"}
			g.Expect(mgr.GetAPIReader().Get(ctx, key, &route)).To(Succeed())
			g.Expect(route.Spec.Hostnames).To(ConsistOf(gatewayv1.Hostname("optimize-orders." + ns + ".example.com")))
			g.Expect(summaryValue(readRequest(g, request), "Routes accepted").Value).To(Equal("3 of 3"))
		}, timeout).Should(Succeed())

		Expect(k8s.Delete(ctx, optimize)).To(Succeed())

		Eventually(func(g Gomega) {
			var route gatewayv1.HTTPRoute
			key := client.ObjectKey{Namespace: ns, Name: "orders-access-optimize"}
			err := mgr.GetAPIReader().Get(ctx, key, &route)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the Optimize route still exists: %v", err)
			g.Expect(summaryValue(readRequest(g, request), "Routes accepted").Value).To(Equal("2 of 2"))
		}, timeout).Should(Succeed())
	})

	It("routes a management plane on its host", func() {
		ns := newNamespace()
		plane := newPlane(ns, "platform")
		Expect(k8s.Create(ctx, plane)).To(Succeed())
		Expect(publishEndpoints(ctx, k8s, plane, planeEndpoints("platform"))).To(Succeed())

		target := pluginsv1alpha1.TargetRef{Kind: "CamundaManagementCluster", Name: "platform"}
		request := accessRequest(ns, "platform-access", target, gatewayParams())
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(condition(readRequest(g, request), v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
			http, grpc := routesOf(g, request)
			g.Expect(grpc).To(BeEmpty())
			g.Expect(http).To(HaveLen(1))
			g.Expect(http[0].Name).To(Equal("platform-access"))
			g.Expect(http[0].Spec.Hostnames).
				To(ConsistOf(gatewayv1.Hostname("management-platform." + ns + ".example.com")))
			g.Expect(http[0].Spec.Rules).To(HaveLen(5))
		}, timeout).Should(Succeed())
	})

	It("refuses a target kind that it does not serve", func() {
		ns := newNamespace()
		target := pluginsv1alpha1.TargetRef{Kind: "ElasticsearchCluster", Name: "search"}
		request := accessRequest(ns, "search-access", target, gatewayParams())
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			read := readRequest(g, request)
			for _, conditionType := range []string{pluginsv1alpha1.ConditionOutputsReady, v1.ConditionReady} {
				c := condition(read, conditionType)
				g.Expect(c.Status).To(Equal(metav1.ConditionFalse), conditionType)
				g.Expect(c.Reason).To(Equal("UnsupportedTarget"), conditionType)
			}
		}, timeout).Should(Succeed())
	})

	It("refuses a pattern that gives a host that is not a DNS name", func() {
		ns := "n" + strings.Repeat("b", 29)
		Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		params := gatewayParams()
		params.HostnamePattern = "{name}-{namespace}.example.com"
		claim := strings.Repeat("a", 40)
		request := accessRequest(ns, "long-access", clusterTarget(claim), params)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			read := readRequest(g, request)
			for _, conditionType := range []string{pluginsv1alpha1.ConditionOutputsReady, v1.ConditionReady} {
				c := condition(read, conditionType)
				g.Expect(c.Status).To(Equal(metav1.ConditionFalse), conditionType)
				g.Expect(c.Reason).To(Equal(pluginsv1alpha1.ReasonInvalidParameters), conditionType)
				g.Expect(c.Message).To(ContainSubstring("hostnamePattern"), conditionType)
			}
		}, timeout).Should(Succeed())
	})

	It("deletes every route with the request", func() {
		ns := newNamespace()
		request := readyClusterRequest(ns)
		optimize := newOptimize(ns, "analytics", "orders")
		Expect(k8s.Create(ctx, optimize)).To(Succeed())
		Expect(publishEndpoints(ctx, k8s, optimize, []v1.Endpoint{
			httpEndpoint("optimize", "analytics-optimize-webapp", 8090, "/"),
		})).To(Succeed())
		Eventually(func(g Gomega) {
			http, _ := routesOf(g, request)
			g.Expect(http).To(HaveLen(2))
		}, timeout).Should(Succeed())

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			err := mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), &pluginsv1alpha1.PluginRequest{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the request still exists: %v", err)
			http, grpc := routesOf(g, request)
			g.Expect(http).To(BeEmpty())
			g.Expect(grpc).To(BeEmpty())
		}, timeout).Should(Succeed())
	})

	It("keeps the request until a route that a finalizer holds is gone", func() {
		const finalizer = "example.com/hold"
		ns := newNamespace()
		request := readyClusterRequest(ns)

		var route gatewayv1.HTTPRoute
		key := client.ObjectKey{Namespace: ns, Name: "orders-access"}
		Expect(k8s.Get(ctx, key, &route)).To(Succeed())
		route.Finalizers = append(route.Finalizers, finalizer)
		Expect(k8s.Update(ctx, &route)).To(Succeed())

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(mgr.GetAPIReader().Get(ctx, key, &route)).To(Succeed())
			g.Expect(route.DeletionTimestamp).NotTo(BeNil())
		}, timeout).Should(Succeed())
		Consistently(func(g Gomega) {
			read := readRequest(g, request)
			g.Expect(read.Finalizers).To(ContainElement(pluginsv1alpha1.FinalizerCleanup))
		}, 3*time.Second).Should(Succeed())

		Expect(k8s.Get(ctx, key, &route)).To(Succeed())
		route.Finalizers = nil
		Expect(k8s.Update(ctx, &route)).To(Succeed())

		Eventually(func(g Gomega) {
			err := mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), &pluginsv1alpha1.PluginRequest{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the request still exists: %v", err)
		}, timeout).Should(Succeed())
	})

	It("is Ready again after an accepted route changes", func() {
		ns := newNamespace()
		request := readyClusterRequest(ns)
		cluster := newCluster(ns, "orders")
		Expect(k8s.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
		before := condition(readRequest(Default, request), v1.ConditionReady).LastTransitionTime

		// LastTransitionTime has a resolution of one second, so a transition
		// in the same second as before shows the same time.
		Eventually(time.Now).Should(BeTemporally(">=", before.Add(time.Second)))
		Expect(publishEndpoints(ctx, k8s, cluster, standaloneEndpoints("orders")[:6])).To(Succeed())

		Eventually(func(g Gomega) {
			var route gatewayv1.HTTPRoute
			g.Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKey{Namespace: ns, Name: "orders-access"}, &route)).
				To(Succeed())
			g.Expect(route.Spec.Rules).To(HaveLen(5))
			g.Expect(route.Generation).To(BeNumerically(">", 1))
			ready := condition(readRequest(g, request), v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(ready.LastTransitionTime.Time).To(BeTemporally(">", before.Time))
		}, timeout).Should(Succeed())
	})

	It("reports GatewayNotFound when only its Gateway is deleted", func() {
		ns := newNamespace()
		spare := publicGateway()
		spare.Name = "spare-" + ns
		Expect(k8s.Create(ctx, spare)).To(Succeed())

		cluster := newCluster(ns, "orders")
		Expect(k8s.Create(ctx, cluster)).To(Succeed())
		Expect(publishEndpoints(ctx, k8s, cluster, standaloneEndpoints("orders"))).To(Succeed())
		params := gatewayParams()
		params.GatewayRef.Name = spare.Name
		request := accessRequest(ns, "spare-access", clusterTarget("orders"), params)
		Expect(k8s.Create(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(condition(readRequest(g, request), v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		}, timeout).Should(Succeed())

		// The routes keep their status, so only the watch of the Gateway
		// can reconcile the request.
		Expect(k8s.Delete(ctx, spare)).To(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(readRequest(g, request), v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(gatewayaccess.ReasonGatewayNotFound))
			g.Expect(ready.Message).To(ContainSubstring("the Gateway gateway-system/" + spare.Name + " does not exist"))
		}, timeout).Should(Succeed())
	})

	It("applies one policy for each route, and is Ready when the Gateway accepts them", func() {
		ns := newNamespace()
		ordersCluster(ns)
		request := policyRequest(ns, "orders-access", policyParams(examplePolicyTemplate()), "10.0.0.0/8")
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			read := readRequest(g, request)
			g.Expect(condition(read, v1.ConditionReady).Reason).To(Equal(v1.ReasonHealthy))
			g.Expect(summaryValue(read, "Policies accepted").Value).To(Equal("2 of 2"))
		}, timeout).Should(Succeed())

		read := readRequest(Default, request)
		policies := policiesOf(Default, request)
		Expect(policies).To(HaveLen(2))
		for name, target := range map[string]map[string]any{
			"orders-access-policy-0":      {"group": gatewayv1.GroupName, "kind": "HTTPRoute", "name": "orders-access"},
			"orders-access-grpc-policy-0": {"group": gatewayv1.GroupName, "kind": "GRPCRoute", "name": "orders-access-grpc"},
		} {
			policy := policyByName(policies, name)
			targets, _, _ := unstructured.NestedSlice(policy.Object, "spec", "targetRefs")
			Expect(targets).To(Equal([]any{target}), name)
			allowed, _, _ := unstructured.NestedStringSlice(policy.Object, "spec", "allowed")
			Expect(allowed).To(Equal([]string{"10.0.0.0/8"}), name)
			Expect(policy.GetLabels()).To(HaveKeyWithValue(pluginsv1alpha1.LabelRequest, "orders-access"), name)
			Expect(policy.GetLabels()).To(HaveKeyWithValue(pluginsv1alpha1.LabelRegistration, "gateway-public"), name)
			Expect(policy.GetOwnerReferences()).To(ContainElement(HaveField("UID", read.UID)), name)
		}

		Expect(read.Status.Resources).To(HaveLen(4))
		Expect(read.Status.Resources).To(ContainElements(
			HaveField("Name", "orders-access-policy-0"),
			HaveField("Name", "orders-access-grpc-policy-0"),
		))
		for _, r := range read.Status.Resources {
			Expect(r.OnOrphan).To(Equal(pluginsv1alpha1.OnOrphanDeleted))
		}
	})

	It("reports the reason of a rejected policy", func() {
		ns := newNamespace()
		policyVerdicts.Store("refused-access-policy-0", verdictRefuse)
		DeferCleanup(func() { policyVerdicts.Delete("refused-access-policy-0") })
		ordersCluster(ns)
		request := policyRequest(ns, "refused-access", policyParams(examplePolicyTemplate()), "10.0.0.0/8")
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(readRequest(g, request), v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal("Invalid"))
			g.Expect(ready.Message).To(ContainSubstring("ExamplePolicy refused-access-policy-0"))
			g.Expect(ready.Message).To(ContainSubstring("the fake Gateway refuses the policy"))
		}, timeout).Should(Succeed())
	})

	It("is Pending while no controller reports on a policy", func() {
		ns := newNamespace()
		policyVerdicts.Store("silent-access-policy-0", verdictIgnore)
		DeferCleanup(func() { policyVerdicts.Delete("silent-access-policy-0") })
		ordersCluster(ns)
		request := policyRequest(ns, "silent-access", policyParams(examplePolicyTemplate()), "10.0.0.0/8")
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			read := readRequest(g, request)
			ready := condition(read, v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal("Pending"))
			g.Expect(ready.Message).To(ContainSubstring("silent-access-policy-0"))
			g.Expect(condition(read, pluginsv1alpha1.ConditionOutputsReady).Status).To(Equal(metav1.ConditionTrue))
			g.Expect(summaryValue(read, "Policies accepted").Value).To(Equal("1 of 2"))
		}, timeout).Should(Succeed())
	})

	It("refuses a template of a kind that the chart does not list, and writes no route", func() {
		ns := newNamespace()
		ordersCluster(ns)
		template := examplePolicyTemplate()
		template.Kind = "OtherPolicy"
		request := policyRequest(ns, "other-access", policyParams(template), "10.0.0.0/8")
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			read := readRequest(g, request)
			for _, conditionType := range []string{pluginsv1alpha1.ConditionOutputsReady, v1.ConditionReady} {
				c := condition(read, conditionType)
				g.Expect(c.Status).To(Equal(metav1.ConditionFalse), conditionType)
				g.Expect(c.Reason).To(Equal(gatewayaccess.ReasonPolicyKindNotAllowed), conditionType)
			}
		}, timeout).Should(Succeed())

		Consistently(func(g Gomega) {
			http, grpc := routesOf(g, request)
			g.Expect(http).To(BeEmpty())
			g.Expect(grpc).To(BeEmpty())
		}, 2*time.Second).Should(Succeed())
	})

	It("refuses a template of a listed kind that the cluster does not serve", func() {
		ns := newNamespace()
		ordersCluster(ns)
		template := examplePolicyTemplate()
		template.Kind = missingPolicyKind.Kind
		request := policyRequest(ns, "missing-access", policyParams(template), "10.0.0.0/8")
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(readRequest(g, request), v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(gatewayaccess.ReasonPolicyKindNotInstalled))
			g.Expect(ready.Message).To(ContainSubstring("restart"))
		}, timeout).Should(Succeed())

		Consistently(func(g Gomega) {
			http, grpc := routesOf(g, request)
			g.Expect(http).To(BeEmpty())
			g.Expect(grpc).To(BeEmpty())
		}, 2*time.Second).Should(Succeed())
	})

	It("writes no route when the API server refuses a policy", func() {
		ns := newNamespace()
		ordersCluster(ns)
		request := policyRequest(
			ns,
			"toomany-access",
			policyParams(examplePolicyTemplate()),
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
		)
		Expect(k8s.Create(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(readRequest(g, request), v1.ConditionReady)
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Message).To(ContainSubstring("must have at most 2 items"))
		}, timeout).Should(Succeed())

		Consistently(func(g Gomega) {
			http, grpc := routesOf(g, request)
			g.Expect(http).To(BeEmpty())
			g.Expect(grpc).To(BeEmpty())
		}, 2*time.Second).Should(Succeed())
	})

	It("deletes the policy of a route that goes", func() {
		ns := newNamespace()
		ordersCluster(ns)
		request := policyRequest(ns, "orders-access", policyParams(examplePolicyTemplate()), "10.0.0.0/8")
		Expect(k8s.Create(ctx, request)).To(Succeed())
		optimize := newOptimize(ns, "analytics", "orders")
		Expect(k8s.Create(ctx, optimize)).To(Succeed())
		Expect(publishEndpoints(ctx, k8s, optimize, []v1.Endpoint{
			httpEndpoint("optimize", "analytics-optimize-webapp", 8090, "/"),
		})).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(policiesOf(g, request)).To(HaveLen(3))
			g.Expect(policyExists(g, ns, "orders-access-optimize-policy-0")).To(BeTrue())
		}, timeout).Should(Succeed())

		Expect(k8s.Delete(ctx, optimize)).To(Succeed())

		Eventually(func(g Gomega) {
			err := mgr.GetAPIReader().Get(ctx, client.ObjectKey{Namespace: ns, Name: "orders-access-optimize"},
				&gatewayv1.HTTPRoute{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the Optimize route still exists: %v", err)
			g.Expect(policyExists(g, ns, "orders-access-optimize-policy-0")).To(BeFalse())
			g.Expect(policiesOf(g, request)).To(HaveLen(2))
		}, timeout).Should(Succeed())
	})

	It("deletes the policies of a template that the parameters drop", func() {
		ns := newNamespace()
		ordersCluster(ns)
		request := policyRequest(ns, "orders-access", policyParams(examplePolicyTemplate()), "10.0.0.0/8")
		Expect(k8s.Create(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(policiesOf(g, request)).To(HaveLen(2))
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			var current pluginsv1alpha1.PluginRequest
			g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(request), &current)).To(Succeed())
			current.Spec.Parameters = rawParams(gatewayParams())
			g.Expect(k8s.Update(ctx, &current)).To(Succeed())
		}, timeout).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(policiesOf(g, request)).To(BeEmpty())
			http, grpc := routesOf(g, request)
			g.Expect(http).To(HaveLen(1))
			g.Expect(grpc).To(HaveLen(1))
			read := readRequest(g, request)
			g.Expect(read.Status.ObservedGeneration).To(Equal(read.Generation))
			g.Expect(condition(read, v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		}, timeout).Should(Succeed())
	})

	It("keeps the policies until every route is gone", func() {
		const finalizer = "example.com/hold"
		ns := newNamespace()
		ordersCluster(ns)
		request := policyRequest(ns, "orders-access", policyParams(examplePolicyTemplate()), "10.0.0.0/8")
		Expect(k8s.Create(ctx, request)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(condition(readRequest(g, request), v1.ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		}, timeout).Should(Succeed())

		var route gatewayv1.HTTPRoute
		key := client.ObjectKey{Namespace: ns, Name: "orders-access"}
		Expect(k8s.Get(ctx, key, &route)).To(Succeed())
		route.Finalizers = append(route.Finalizers, finalizer)
		Expect(k8s.Update(ctx, &route)).To(Succeed())

		Expect(k8s.Delete(ctx, request)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(mgr.GetAPIReader().Get(ctx, key, &route)).To(Succeed())
			g.Expect(route.DeletionTimestamp).NotTo(BeNil())
		}, timeout).Should(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(policyExists(g, ns, "orders-access-policy-0")).To(BeTrue())
		}, 3*time.Second).Should(Succeed())

		Expect(k8s.Get(ctx, key, &route)).To(Succeed())
		route.Finalizers = nil
		Expect(k8s.Update(ctx, &route)).To(Succeed())

		Eventually(func(g Gomega) {
			err := mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), &pluginsv1alpha1.PluginRequest{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the request still exists: %v", err)
			http, grpc := routesOf(g, request)
			g.Expect(http).To(BeEmpty())
			g.Expect(grpc).To(BeEmpty())
			g.Expect(policiesOf(g, request)).To(BeEmpty())
		}, timeout).Should(Succeed())
	})
})
