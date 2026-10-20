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

package skeleton_test

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

var _ = Describe("the skeleton manager", func() {
	It("reads the registrations of the cluster through its cache", func() {
		Eventually(func() error {
			return mgr.GetClient().List(ctx, &pluginsv1alpha1.PluginRegistrationList{})
		}).WithTimeout(10 * time.Second).Should(Succeed())
	})

	It("writes a registration that the CRD of the pinned api module keeps whole", func() {
		registration := &pluginsv1alpha1.PluginRegistration{
			ObjectMeta: metav1.ObjectMeta{Name: "skeleton-round-trip"},
			Spec: pluginsv1alpha1.PluginRegistrationSpec{
				Sections: []string{"backup-storage"},
				Label:    "Skeleton",
				Mode:     pluginsv1alpha1.ModeOptional,
				ServiceAccount: pluginsv1alpha1.ServiceAccountRef{
					Namespace: "camunda-operator-plugins",
					Name:      "skeleton",
				},
				OperatorVersions: ">=0.3.0 <1.0.0",
				Parameters: &apiextensionsv1.JSON{
					Raw: []byte(`{"type":"object","properties":{"size":{"type":"integer"}}}`),
				},
				ClaimEditable:    []string{"size"},
				DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{pluginsv1alpha1.DeletionPolicyDelete},
			},
		}
		want := registration.Spec.DeepCopy()

		Expect(mgr.GetClient().Create(ctx, registration)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(mgr.GetClient().Delete(ctx, registration))).To(Succeed())
		})

		var read pluginsv1alpha1.PluginRegistration
		Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(registration), &read)).To(Succeed())

		// The server writes the JSON of the schema back in its own form.
		Expect(read.Spec.Parameters.Raw).To(MatchJSON(want.Parameters.Raw))
		read.Spec.Parameters, want.Parameters = nil, nil
		Expect(read.Spec).To(Equal(*want))
	})

	It("serves a request with one ConfigMap", func() {
		k8s := mgr.GetClient()
		Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}})).To(Succeed())
		request := &pluginsv1alpha1.PluginRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "orders-skeleton",
				Namespace: "team-a",
				Labels: map[string]string{
					pluginsv1alpha1.LabelSection:      "skeleton",
					pluginsv1alpha1.LabelRegistration: "skeleton",
				},
			},
			Spec: pluginsv1alpha1.PluginRequestSpec{
				Section:      "skeleton",
				Registration: "skeleton",
				ClaimRef: pluginsv1alpha1.ClaimRef{
					Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim,
					Name: "orders",
				},
				Target:         pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: "orders"},
				DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
			},
		}
		Expect(k8s.Create(ctx, request)).To(Succeed())

		var read pluginsv1alpha1.PluginRequest
		Eventually(func(g Gomega) {
			g.Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), &read)).To(Succeed())
			g.Expect(meta.IsStatusConditionTrue(read.Status.Conditions, "OutputsReady")).To(BeTrue())
			g.Expect(meta.IsStatusConditionTrue(read.Status.Conditions, "Ready")).To(BeTrue())
		}).WithTimeout(20 * time.Second).Should(Succeed())

		var configMap corev1.ConfigMap
		key := client.ObjectKey{Namespace: "team-a", Name: read.Status.Outputs["configMapName"]}
		Expect(mgr.GetAPIReader().Get(ctx, key, &configMap)).To(Succeed())
		Expect(configMap.Labels).To(HaveKeyWithValue(pluginsv1alpha1.LabelRequest, "orders-skeleton"))
		Expect(configMap.Labels).To(HaveKeyWithValue(pluginsv1alpha1.LabelRegistration, "skeleton"))
	})

	It("refuses a registration that the CRD refuses", func() {
		registration := &pluginsv1alpha1.PluginRegistration{
			ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 64)},
			Spec: pluginsv1alpha1.PluginRegistrationSpec{
				Sections: []string{"backup-storage"},
				Label:    "Skeleton",
				Mode:     pluginsv1alpha1.ModeOptional,
				ServiceAccount: pluginsv1alpha1.ServiceAccountRef{
					Namespace: "camunda-operator-plugins",
					Name:      "skeleton",
				},
				DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{pluginsv1alpha1.DeletionPolicyDelete},
			},
		}

		Expect(mgr.GetClient().Create(ctx, registration)).
			To(MatchError(ContainSubstring("metadata.name must be at most 63 characters")))
	})
})
