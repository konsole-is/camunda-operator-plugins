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

package pluginkit_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// otherRegistration is a registration that the test plugin does not serve.
const otherRegistration = "other"

// readGrant returns the Grant of the template read of request.
func readGrant(g Gomega, request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.Grant {
	var grant pluginsv1alpha1.Grant
	key := client.ObjectKey{Namespace: request.Namespace, Name: pluginkit.GrantName(request.Name, "read")}
	g.Expect(k8s.Get(ctx, key, &grant)).To(Succeed())
	return &grant
}

func grantExists(request *pluginsv1alpha1.PluginRequest, template string) func() bool {
	return func() bool {
		var grant pluginsv1alpha1.Grant
		key := client.ObjectKey{Namespace: request.Namespace, Name: pluginkit.GrantName(request.Name, template)}
		return !apierrors.IsNotFound(k8s.Get(ctx, key, &grant))
	}
}

// grantRow matches the status.resources row of the Grant of template of
// request.
func grantRow(request *pluginsv1alpha1.PluginRequest, template string) OmegaMatcher {
	return SatisfyAll(
		HaveField("Kind", "Grant"),
		HaveField("Name", pluginkit.GrantName(request.Name, template)),
		HaveField("OnOrphan", pluginsv1alpha1.OnOrphanKept),
	)
}

// grantEvents returns the events on request whose related object is the
// Grant of template.
func grantEvents(g Gomega, request *pluginsv1alpha1.PluginRequest, template string) []eventsv1.Event {
	current := readRequest(g, request)
	var list eventsv1.EventList
	g.Expect(k8s.List(ctx, &list, client.InNamespace(request.Namespace))).To(Succeed())

	var found []eventsv1.Event
	for _, e := range list.Items {
		if e.Regarding.UID == current.UID && e.Related != nil && e.Related.Kind == "Grant" &&
			e.Related.Name == pluginkit.GrantName(request.Name, template) {
			found = append(found, e)
		}
	}
	return found
}

func condition(g Gomega, request *pluginsv1alpha1.PluginRequest, conditionType string) metav1.Condition {
	cond := meta.FindStatusCondition(readRequest(g, request).Status.Conditions, conditionType)
	g.Expect(cond).NotTo(BeNil(), "the request has no condition %s", conditionType)
	return *cond
}

// settledReconciles waits until the test plugin serves no more requests in
// namespace for a second, and returns its count of reconciles there.
func settledReconciles(namespace string) int {
	GinkgoHelper()
	var count int
	Eventually(func() bool {
		before := plugin.reconcileCount(namespace)
		time.Sleep(time.Second)
		count = plugin.reconcileCount(namespace)
		return count == before
	}).Should(BeTrue())
	return count
}

var _ = Describe("the request reconciler", func() {
	It("writes the registration with Ready before it serves the first request", func() {
		request := &pluginsv1alpha1.PluginRequest{
			ObjectMeta: metav1.ObjectMeta{Namespace: servedFirstNamespace, Name: testRequestName},
		}

		Eventually(func(g Gomega) {
			g.Expect(readRequest(g, request).Finalizers).To(ContainElement(pluginsv1alpha1.FinalizerCleanup))
		}).Should(Succeed())
		Eventually(plugin.firstSawRegistration).ShouldNot(BeZero())
		Expect(plugin.firstSawRegistration()).To(Equal(1))
	})

	It("reports OutputsReady, then Ready", func() {
		plugin.setNotReady(&pluginkit.Waiting{Reason: pluginsv1alpha1.ReasonWaitingForEndpoints, Message: "waiting"})
		DeferCleanup(func() { plugin.setNotReady(nil) })
		request := createRequest("outputs-then-ready")

		Eventually(func(g Gomega) {
			readyGrants(g, request)
			current := readRequest(g, request)
			outputs := meta.FindStatusCondition(current.Status.Conditions, pluginsv1alpha1.ConditionOutputsReady)
			ready := meta.FindStatusCondition(current.Status.Conditions, "Ready")
			g.Expect(outputs).NotTo(BeNil())
			g.Expect(ready).NotTo(BeNil())
			g.Expect(outputs.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(outputs.Reason).To(Equal("Healthy"))
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(pluginsv1alpha1.ReasonWaitingForEndpoints))
			g.Expect(current.Status.Outputs).To(HaveKeyWithValue("configMap", testRequestName+"-data"))
			g.Expect(current.Status.ObservedGeneration).To(Equal(current.Generation))
		}).Should(Succeed())

		plugin.setNotReady(nil)
		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) { r.Labels["touch"] = "1" })

		Eventually(func(g Gomega) {
			ready := condition(g, request, "Ready")
			g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(ready.Reason).To(Equal("Healthy"))
		}).Should(Succeed())
	})

	It("waits for its grants before it reports Ready", func() {
		request := createRequest("grant-waiting")

		Eventually(func(g Gomega) {
			ready := condition(g, request, "Ready")
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(pluginsv1alpha1.ReasonWaitingForGrants))
			g.Expect(ready.Message).To(ContainSubstring(pluginkit.GrantName(request.Name, "read")))
			g.Expect(condition(g, request, pluginsv1alpha1.ConditionOutputsReady).Status).
				To(Equal(metav1.ConditionTrue))
			g.Expect(readRequest(g, request).Status.Outputs).To(HaveKeyWithValue("configMap", testRequestName+"-data"))
		}).Should(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(condition(g, request, "Ready").Reason).To(Equal(pluginsv1alpha1.ReasonWaitingForGrants))
		}).WithTimeout(time.Second).Should(Succeed())

		// The spec plays the identity extension. Only a reconcile that read
		// this Grant status can write Ready=True.
		Eventually(func(g Gomega) { readyGrants(g, request) }).Should(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(g, request, "Ready")
			g.Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(ready.Reason).To(Equal(v1.ReasonHealthy))
		}).Should(Succeed())
	})

	It("keeps the outputs and reports the summary of a pending request", func() {
		request := createRequest("pending")
		waitReady(request)
		plugin.setPending(&pluginkit.Waiting{Reason: "WaitingForBucket", Message: "creating"})
		DeferCleanup(func() { plugin.setPending(nil) })

		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) { r.Labels["touch"] = "1" })

		Eventually(func(g Gomega) {
			current := readRequest(g, request)
			outputs := meta.FindStatusCondition(current.Status.Conditions, pluginsv1alpha1.ConditionOutputsReady)
			g.Expect(outputs).NotTo(BeNil())
			g.Expect(outputs.Reason).To(Equal("WaitingForBucket"))
			g.Expect(current.Status.Outputs).To(HaveKeyWithValue("configMap", testRequestName+"-data"))
			g.Expect(current.Status.Summary).To(ConsistOf(HaveField("Label", "Size")))
			g.Expect(current.Status.Resources).To(ConsistOf(grantRow(request, "read")))
		}).Should(Succeed())
	})

	It("reports the summary of a request that was never served", func() {
		plugin.setPending(&pluginkit.Waiting{Reason: "WaitingForBucket", Message: "creating"})
		DeferCleanup(func() { plugin.setPending(nil) })
		request := createRequest("pending-first")

		Eventually(func(g Gomega) {
			current := readRequest(g, request)
			g.Expect(condition(g, request, "Ready").Reason).To(Equal("WaitingForBucket"))
			g.Expect(current.Status.Outputs).To(BeEmpty())
			g.Expect(current.Status.Summary).To(ConsistOf(HaveField("Label", "Size")))
		}).Should(Succeed())
	})

	It("labels and owns what it applies", func() {
		request := createRequest("labels-and-owns")
		waitReady(request)
		current := readRequest(Default, request)
		wantLabels := map[string]string{
			pluginsv1alpha1.LabelRegistration: testRegistrationName,
			pluginsv1alpha1.LabelRequest:      testRequestName,
		}

		var cm corev1.ConfigMap
		Expect(k8s.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: testRequestName + "-data"}, &cm)).
			To(Succeed())
		Expect(cm.Labels).To(Equal(wantLabels))
		Expect(cm.OwnerReferences).To(ConsistOf(SatisfyAll(
			HaveField(
				"Kind",
				"PluginRequest",
			),
			HaveField("UID", current.UID),
			HaveField("Controller", HaveValue(BeTrue())),
		)))
		Expect(cm.Data).To(HaveKeyWithValue("policy", "trust orders-camunda"))

		var sc storagev1.StorageClass
		Expect(k8s.Get(ctx, client.ObjectKey{Name: storageClassName(request)}, &sc)).To(Succeed())
		Expect(sc.Labels).To(Equal(map[string]string{
			pluginsv1alpha1.LabelRegistration:     testRegistrationName,
			pluginsv1alpha1.LabelRequest:          testRequestName,
			pluginsv1alpha1.LabelRequestNamespace: request.Namespace,
		}))
		Expect(sc.OwnerReferences).To(BeEmpty())
	})

	It("labels the cluster-scoped objects of two requests of the same name with their namespaces", func() {
		a := createRequest("same-name-a")
		b := createRequest("same-name-b")
		waitReady(a)
		waitReady(b)

		var scA, scB storagev1.StorageClass
		Expect(k8s.Get(ctx, client.ObjectKey{Name: storageClassName(a)}, &scA)).To(Succeed())
		Expect(k8s.Get(ctx, client.ObjectKey{Name: storageClassName(b)}, &scB)).To(Succeed())
		Expect(scA.Labels).To(HaveKeyWithValue(pluginsv1alpha1.LabelRequest, testRequestName))
		Expect(scB.Labels).To(HaveKeyWithValue(pluginsv1alpha1.LabelRequest, testRequestName))
		Expect(scA.Labels).To(HaveKeyWithValue(pluginsv1alpha1.LabelRequestNamespace, "same-name-a"))
		Expect(scB.Labels).To(HaveKeyWithValue(pluginsv1alpha1.LabelRequestNamespace, "same-name-b"))
	})

	It("reports a grant that is not ready as GrantRejected and keeps the outputs", func() {
		request := createRequest("grant-rejected")
		waitReady(request)
		current := readRequest(Default, request)

		grant := readGrant(Default, request)
		Expect(grant.Labels).To(Equal(map[string]string{
			pluginsv1alpha1.LabelRegistration: testRegistrationName,
			pluginsv1alpha1.LabelRequest:      testRequestName,
			pluginsv1alpha1.LabelCluster:      "orders",
		}))
		Expect(grant.OwnerReferences).To(ConsistOf(HaveField("UID", current.UID)))
		Expect(grant.Spec.Fragment.Raw).To(MatchJSON(
			`{"Sid":"trust orders-camunda","Action":"s3:GetObject","Resource":"orders-test-data"}`,
		))

		// The spec plays the identity extension. Only a reconcile that read
		// this Grant status can write GrantRejected.
		Eventually(func(g Gomega) {
			grant := readGrant(g, request)
			meta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
				Type: "Ready", Status: metav1.ConditionFalse, Reason: pluginsv1alpha1.ReasonOverCeiling,
				Message: "the statement asks for more than the ceiling",
			})
			g.Expect(k8s.Status().Update(ctx, grant)).To(Succeed())
		}).Should(Succeed())

		Eventually(func(g Gomega) {
			ready := condition(g, request, "Ready")
			g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(ready.Reason).To(Equal(pluginsv1alpha1.ReasonGrantRejected))
			g.Expect(ready.Message).To(ContainSubstring(pluginsv1alpha1.ReasonOverCeiling))
			g.Expect(condition(g, request, pluginsv1alpha1.ConditionOutputsReady).Status).
				To(Equal(metav1.ConditionTrue))
		}).Should(Succeed())
	})

	It("writes a grant only when its required inputs have values, and deletes it when they lose them", func() {
		request := createRequest("grant-inputs")
		waitReady(request)
		Consistently(grantExists(request, "key-use")).WithTimeout(time.Second).Should(BeFalse())
		Expect(readRequest(Default, request).Status.Resources).To(ConsistOf(grantRow(request, "read")))

		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Inputs = map[string]string{"key": "arn:aws:kms:eu-west-1:1:key/k"}
		})
		Eventually(grantExists(request, "key-use")).Should(BeTrue())

		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Inputs = nil })
		Eventually(grantExists(request, "key-use")).Should(BeFalse())
		Expect(grantExists(request, "read")()).To(BeTrue())
		Eventually(func(g Gomega) {
			g.Expect(readRequest(g, request).Status.Resources).To(ConsistOf(grantRow(request, "read")))
		}).Should(Succeed())
	})

	It("lists each grant in status.resources and records an apply event for it", func() {
		request := createRequest("grant-resources", func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Inputs = map[string]string{"key": "arn:aws:kms:eu-west-1:1:key/k"}
		})
		waitReady(request)

		Eventually(func(g Gomega) {
			g.Expect(readRequest(g, request).Status.Resources).To(ConsistOf(
				grantRow(request, "read"),
				grantRow(request, "key-use"),
			))
			for _, template := range []string{"read", "key-use"} {
				g.Expect(grantEvents(g, request, template)).To(ContainElement(SatisfyAll(
					HaveField("Type", corev1.EventTypeNormal),
					HaveField("Action", "Create"),
				)))
			}
		}).Should(Succeed())
	})

	It("reports a rejected grant that is still deleting", func() {
		request := createRequest("grant-deleting", func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Inputs = map[string]string{"key": "k"}
		})
		waitReady(request)
		key := client.ObjectKey{Namespace: request.Namespace, Name: pluginkit.GrantName(request.Name, "key-use")}
		Eventually(func(g Gomega) {
			var grant pluginsv1alpha1.Grant
			g.Expect(k8s.Get(ctx, key, &grant)).To(Succeed())
			controllerutil.AddFinalizer(&grant, "example.com/hold")
			g.Expect(k8s.Update(ctx, &grant)).To(Succeed())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			var grant pluginsv1alpha1.Grant
			g.Expect(k8s.Get(ctx, key, &grant)).To(Succeed())
			meta.SetStatusCondition(&grant.Status.Conditions, metav1.Condition{
				Type: "Ready", Status: metav1.ConditionFalse, Reason: pluginsv1alpha1.ReasonRejected, Message: "no",
			})
			g.Expect(k8s.Status().Update(ctx, &grant)).To(Succeed())
		}).Should(Succeed())

		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) { r.Spec.Inputs = nil })

		Eventually(func(g Gomega) {
			var grant pluginsv1alpha1.Grant
			g.Expect(k8s.Get(ctx, key, &grant)).To(Succeed())
			g.Expect(grant.DeletionTimestamp).NotTo(BeNil())
			g.Expect(condition(g, request, "Ready").Reason).To(Equal(pluginsv1alpha1.ReasonGrantRejected))
		}).Should(Succeed())

		// Without its owner reference, only the request label ties the Grant
		// to the request when it goes.
		Eventually(func(g Gomega) {
			var grant pluginsv1alpha1.Grant
			g.Expect(k8s.Get(ctx, key, &grant)).To(Succeed())
			grant.OwnerReferences = nil
			g.Expect(k8s.Update(ctx, &grant)).To(Succeed())
		}).Should(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(condition(g, request, "Ready").Reason).To(Equal(pluginsv1alpha1.ReasonGrantRejected))
		}).WithTimeout(time.Second).Should(Succeed())

		Eventually(func(g Gomega) {
			var grant pluginsv1alpha1.Grant
			g.Expect(k8s.Get(ctx, key, &grant)).To(Succeed())
			controllerutil.RemoveFinalizer(&grant, "example.com/hold")
			g.Expect(k8s.Update(ctx, &grant)).To(Succeed())
		}).Should(Succeed())
		waitReady(request)
	})

	DescribeTable("refuses parameters that do not pass the schema",
		func(namespace, parameters, message string) {
			request := createRequest(namespace, func(r *pluginsv1alpha1.PluginRequest) {
				r.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(parameters)}
			})

			Eventually(func(g Gomega) {
				for _, conditionType := range []string{pluginsv1alpha1.ConditionOutputsReady, "Ready"} {
					cond := condition(g, request, conditionType)
					g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(Equal(pluginsv1alpha1.ReasonInvalidParameters))
					g.Expect(cond.Message).To(ContainSubstring(message))
				}
			}).Should(Succeed())

			Consistently(func() bool {
				var cm corev1.ConfigMap
				key := client.ObjectKey{Namespace: request.Namespace, Name: testRequestName + "-data"}
				return apierrors.IsNotFound(k8s.Get(ctx, key, &cm))
			}).WithTimeout(2 * time.Second).Should(BeTrue())
		},
		Entry("a field that fails the schema", "invalid-parameters", `{"size":0}`, "spec.parameters.size"),
		Entry("a rule of the schema", "rule-parameters", `{"size":20}`, "size must be at most 10"),
	)

	It("keeps the resourceVersion of a served request while nothing changes", func() {
		request := createRequest("steady")
		waitReady(request)
		Eventually(func(g Gomega) {
			g.Expect(readRequest(g, request).Status.SummaryTime).NotTo(BeNil())
		}).Should(Succeed())
		version := readRequest(Default, request).ResourceVersion

		Consistently(func(g Gomega) {
			g.Expect(readRequest(g, request).ResourceVersion).To(Equal(version))
		}).WithTimeout(3 * time.Second).Should(Succeed())

		// summaryTime has whole seconds, so a reconcile in a later second
		// would write a new one if it saw the stored summary as changed.
		summaryTime := readRequest(Default, request).Status.SummaryTime
		updateRequest(request, func(r *pluginsv1alpha1.PluginRequest) { r.Labels["touch"] = "1" })
		Consistently(func(g Gomega) {
			g.Expect(readRequest(g, request).Status.SummaryTime).To(Equal(summaryTime))
		}).WithTimeout(2 * time.Second).Should(Succeed())
	})

	It("ignores a request of another registration", func() {
		request := createRequest("other-registration", func(r *pluginsv1alpha1.PluginRequest) {
			r.Labels[pluginsv1alpha1.LabelRegistration] = otherRegistration
			r.Spec.Registration = otherRegistration
		})

		Consistently(func(g Gomega) {
			g.Expect(readRequest(g, request).Finalizers).To(BeEmpty())
		}).WithTimeout(2 * time.Second).Should(Succeed())
	})

	It("ignores a request of another registration label that a Grant event names", func() {
		request := createRequest("other-label", func(r *pluginsv1alpha1.PluginRequest) {
			r.Labels[pluginsv1alpha1.LabelRegistration] = otherRegistration
		})
		grant := &pluginsv1alpha1.Grant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "stray",
				Namespace: request.Namespace,
				Labels:    map[string]string{pluginsv1alpha1.LabelRequest: request.Name},
			},
			Spec: pluginsv1alpha1.GrantSpec{
				Cluster:  v1.ClusterRef{Name: "orders"},
				Template: "read",
				Format:   pluginsv1alpha1.GrantFormatAWSIAMStatement,
				Fragment: &apiextensionsv1.JSON{Raw: []byte(`{"Action":"s3:GetObject"}`)},
			},
		}
		Expect(k8s.Create(ctx, grant)).To(Succeed())

		Consistently(func(g Gomega) {
			current := readRequest(g, request)
			g.Expect(current.Finalizers).To(BeEmpty())
			g.Expect(current.Status.Conditions).To(BeEmpty())
		}).WithTimeout(2 * time.Second).Should(Succeed())
	})

	It("reconciles a request again when a watched object that it names changes", func() {
		request := createRequest("watched", func(r *pluginsv1alpha1.PluginRequest) {
			r.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"size":1,"policy":"read","secret":"orders-input"}`)}
		})
		waitReady(request)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "orders-input", Namespace: request.Namespace},
			StringData: map[string]string{"value": "first"},
		}
		Expect(k8s.Create(ctx, secret)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(readRequest(g, request).Status.Outputs).To(HaveKeyWithValue("input", "first"))
		}).Should(Succeed())

		Eventually(func(g Gomega) {
			var current corev1.Secret
			g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(secret), &current)).To(Succeed())
			current.StringData = map[string]string{"value": "second"}
			g.Expect(k8s.Update(ctx, &current)).To(Succeed())
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(readRequest(g, request).Status.Outputs).To(HaveKeyWithValue("input", "second"))
		}).Should(Succeed())
	})

	It("starts no reconcile for a watched object that maps to no request", func() {
		request := createRequest("unwatched")
		waitReady(request)
		settled := settledReconciles(request.Namespace)

		Expect(k8s.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: request.Namespace},
		})).To(Succeed())

		Consistently(func() int {
			return plugin.reconcileCount(request.Namespace)
		}).WithTimeout(2 * time.Second).Should(Equal(settled))
	})

	It("logs JSON lines that carry the request key", func() {
		request := createRequest("json-logs")
		waitReady(request)

		var found bool
		for line := range strings.SplitSeq(logs.String(), "\n") {
			if !strings.Contains(line, `"msg":"Served PluginRequest"`) {
				continue
			}
			var decoded map[string]any
			Expect(json.Unmarshal([]byte(line), &decoded)).To(Succeed(), "the line %q is not JSON", line)
			if reflect.DeepEqual(decoded["request"], map[string]any{
				"name": request.Name, "namespace": request.Namespace,
			}) {
				found = true
			}
		}
		Expect(found).To(BeTrue(), "no log line of the request carries the request key")
	})
})
