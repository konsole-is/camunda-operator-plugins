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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

var _ = Describe("DeleteOwned", func() {
	// ownedConfigMap creates the ConfigMap data in a namespace of its own,
	// with labels, and returns a request of that namespace.
	ownedConfigMap := func(ns string, labels map[string]string) (*pluginsv1alpha1.PluginRequest, *corev1.ConfigMap) {
		GinkgoHelper()
		Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "data", Labels: labels}}
		Expect(k8s.Create(ctx, cm)).To(Succeed())

		request := &pluginsv1alpha1.PluginRequest{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "orders-backup"},
			Spec:       pluginsv1alpha1.PluginRequestSpec{Registration: "s3-bucket"},
		}
		return request, cm
	}
	requestLabels := map[string]string{
		pluginsv1alpha1.LabelRegistration: "s3-bucket",
		pluginsv1alpha1.LabelRequest:      "orders-backup",
	}

	It("deletes an object with the labels of the request", func() {
		request, cm := ownedConfigMap("delete-owned", requestLabels)

		Expect(pluginkit.DeleteOwned(ctx, k8s, k8s, request, &corev1.ConfigMap{}, "data")).To(Succeed())

		Expect(gone(cm)()).To(BeTrue())
	})

	It("keeps an object of the name with another request label", func() {
		request, cm := ownedConfigMap("delete-owned-other-request", map[string]string{
			pluginsv1alpha1.LabelRegistration: "s3-bucket",
			pluginsv1alpha1.LabelRequest:      "payments-backup",
		})

		Expect(pluginkit.DeleteOwned(ctx, k8s, k8s, request, &corev1.ConfigMap{}, "data")).To(Succeed())

		Expect(k8s.Get(ctx, client.ObjectKeyFromObject(cm), cm)).To(Succeed())
	})

	It("keeps an object of the name with another registration label", func() {
		request, cm := ownedConfigMap("delete-owned-other-registration", map[string]string{
			pluginsv1alpha1.LabelRegistration: "rustfs-bucket",
			pluginsv1alpha1.LabelRequest:      "orders-backup",
		})

		Expect(pluginkit.DeleteOwned(ctx, k8s, k8s, request, &corev1.ConfigMap{}, "data")).To(Succeed())

		Expect(k8s.Get(ctx, client.ObjectKeyFromObject(cm), cm)).To(Succeed())
	})

	It("is done when the object is missing", func() {
		request, _ := ownedConfigMap("delete-owned-missing", requestLabels)

		Expect(pluginkit.DeleteOwned(ctx, k8s, k8s, request, &corev1.ConfigMap{}, "missing")).To(Succeed())
	})
})
