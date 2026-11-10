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

package awsebs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsebs"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

const (
	pluginNamespace = "camunda-operator-plugins"
	registration    = "aws-ebs-encrypted"
	sectionName     = "volume-encryption"
	claimName       = "orders"
	requestName     = claimName + "-" + sectionName
	testKey         = "arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
)

// chartRegistrations returns the registrations of the values of the chart,
// as the controller loads them from the ConfigMap of the chart.
func chartRegistrations(dir string) ([]pluginsv1alpha1.PluginRegistration, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "charts", "aws-ebs", "values.yaml"))
	if err != nil {
		return nil, err
	}

	var values struct {
		Registrations json.RawMessage `json:"registrations"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "registrations.yaml")
	if err := os.WriteFile(path, values.Registrations, 0o600); err != nil {
		return nil, err
	}
	return pluginkit.LoadRegistrations(path)
}

// encryptionKeySection is the core section encryption-key, whose keyRef is
// the input key of the registration.
func encryptionKeySection() *pluginsv1alpha1.PluginSection {
	return &pluginsv1alpha1.PluginSection{
		ObjectMeta: metav1.ObjectMeta{Name: "encryption-key"},
		Spec: pluginsv1alpha1.PluginSectionSpec{
			Label:            "Encryption key",
			Order:            20,
			Selection:        pluginsv1alpha1.SelectionOne,
			EmptyOptionLabel: "No key",
			AppliesTo:        []pluginsv1alpha1.ClaimKind{pluginsv1alpha1.ClaimKindCamundaClusterClaim},
			Outputs:          []pluginsv1alpha1.OutputDecl{{Key: "keyRef"}, {Key: "format"}},
		},
	}
}

// volumeEncryptionSection is the core section volume-encryption, as the
// operator writes it.
func volumeEncryptionSection() *pluginsv1alpha1.PluginSection {
	return &pluginsv1alpha1.PluginSection{
		ObjectMeta: metav1.ObjectMeta{Name: sectionName},
		Spec: pluginsv1alpha1.PluginSectionSpec{
			Label:            "Volume encryption",
			Order:            30,
			Selection:        pluginsv1alpha1.SelectionOne,
			EmptyOptionLabel: "Default StorageClass",
			Slot:             pluginsv1alpha1.SlotVolumeEncryption,
			AppliesTo:        []pluginsv1alpha1.ClaimKind{pluginsv1alpha1.ClaimKindCamundaClusterClaim},
			Outputs:          []pluginsv1alpha1.OutputDecl{{Key: "storageClassName"}},
		},
	}
}

// installRBAC creates the ServiceAccount sa and the ClusterRoles of
// charts/aws-ebs/templates/rbac.yaml for registrations, bound to sa. The
// conformance suite reads them to find the kinds of the plugin.
func installRBAC(
	ctx context.Context,
	c client.Client,
	sa pluginsv1alpha1.ServiceAccountRef,
	registrations ...string,
) error {
	plugins := pluginsv1alpha1.GroupVersion.Group
	write := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	roles := map[string][]rbacv1.PolicyRule{
		sa.Name + "-serve": {
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"},
				ResourceNames: registrations, Verbs: []string{"serve"}},
		},
		sa.Name + "-controller": {
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"},
				ResourceNames: registrations, Verbs: []string{"get", "create", "patch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations/status"},
				ResourceNames: registrations, Verbs: []string{"update"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginrequests"},
				Verbs: []string{"get", "list", "watch", "update", "patch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginrequests/status", "pluginrequests/finalizers"},
				Verbs: []string{"update"}},
			{APIGroups: []string{plugins}, Resources: []string{"grants"}, Verbs: write},
			{APIGroups: []string{"events.k8s.io"}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
			{APIGroups: []string{storagev1.GroupName}, Resources: []string{"storageclasses"}, Verbs: write},
		},
	}

	if err := c.Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Namespace: sa.Namespace, Name: sa.Name},
	}); err != nil {
		return err
	}

	for name, rules := range roles {
		if err := c.Create(
			ctx,
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: rules},
		); err != nil {
			return err
		}
		if err := c.Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: sa.Namespace, Name: sa.Name}},
		}); err != nil {
			return err
		}
	}
	return nil
}

// acceptRegistration writes Accepted=True on the registration name, in place
// of the registry controller of the operator. It writes again until a read
// shows both Accepted=True and the Ready=True of the plugin, so that a status
// write of the plugin does not hide it.
func acceptRegistration(name string) {
	Eventually(func(g Gomega) {
		var reg pluginsv1alpha1.PluginRegistration
		g.Expect(k8s.Get(ctx, client.ObjectKey{Name: name}, &reg)).To(Succeed())
		g.Expect(meta.IsStatusConditionTrue(reg.Status.Conditions, v1.ConditionReady)).To(BeTrue())

		if !meta.IsStatusConditionTrue(reg.Status.Conditions, pluginsv1alpha1.ConditionAccepted) {
			meta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
				Type:    pluginsv1alpha1.ConditionAccepted,
				Status:  metav1.ConditionTrue,
				Reason:  v1.ReasonHealthy,
				Message: "The suite accepts the registration",
			})
			g.Expect(k8s.Status().Update(ctx, &reg)).To(Succeed())
		}

		var again pluginsv1alpha1.PluginRegistration
		g.Expect(k8s.Get(ctx, client.ObjectKey{Name: name}, &again)).To(Succeed())
		g.Expect(meta.IsStatusConditionTrue(again.Status.Conditions, pluginsv1alpha1.ConditionAccepted)).To(BeTrue())
		g.Expect(meta.IsStatusConditionTrue(again.Status.Conditions, v1.ConditionReady)).To(BeTrue())
	}, time.Minute).Should(Succeed())
}

var namespaceSeq atomic.Int32

// newNamespace creates a namespace of its own for one spec.
func newNamespace() string {
	name := fmt.Sprintf("team-%d", namespaceSeq.Add(1))
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func raw(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

// ebsRequest is the volume encryption request of the claim orders in
// namespace, with key as its input key when key is not empty, params, and
// policy.
func ebsRequest(namespace, key, params string, policy pluginsv1alpha1.DeletionPolicy) *pluginsv1alpha1.PluginRequest {
	request := &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      requestName,
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      sectionName,
				pluginsv1alpha1.LabelRegistration: registration,
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:        sectionName,
			Registration:   registration,
			ClaimRef:       pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: claimName},
			Target:         pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: claimName},
			DeletionPolicy: policy,
		},
	}
	if key != "" {
		request.Spec.Inputs = map[string]string{"key": key}
	}
	if params != "" {
		request.Spec.Parameters = raw(params)
	}
	return request
}

// readRequest returns the current request, or fails the spec when it is gone.
func readRequest(g Gomega, request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.PluginRequest {
	var current pluginsv1alpha1.PluginRequest
	g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(request), &current)).To(Succeed())
	return &current
}

func condition(g Gomega, request *pluginsv1alpha1.PluginRequest, conditionType string) metav1.Condition {
	cond := meta.FindStatusCondition(readRequest(g, request).Status.Conditions, conditionType)
	g.Expect(cond).NotTo(BeNil(), "the request has no condition %s", conditionType)
	return *cond
}

// updateRequest changes the spec of request from a fresh read.
func updateRequest(request *pluginsv1alpha1.PluginRequest, change func(*pluginsv1alpha1.PluginRequest)) {
	Eventually(func(g Gomega) {
		current := readRequest(g, request)
		change(current)
		g.Expect(k8s.Update(ctx, current)).To(Succeed())
	}).Should(Succeed())
}

// liveStorageClass returns the StorageClass of the name of request, or fails
// when it does not exist.
func liveStorageClass(g Gomega, request *pluginsv1alpha1.PluginRequest) *storagev1.StorageClass {
	var sc storagev1.StorageClass
	g.Expect(k8s.Get(ctx, client.ObjectKey{Name: storageClassNameOf(request)}, &sc)).To(Succeed())
	return &sc
}

func storageClassExists(request *pluginsv1alpha1.PluginRequest) func() bool {
	return func() bool {
		var sc storagev1.StorageClass
		err := k8s.Get(ctx, client.ObjectKey{Name: storageClassNameOf(request)}, &sc)
		return !apierrors.IsNotFound(err)
	}
}

func storageClassNameOf(request *pluginsv1alpha1.PluginRequest) string {
	return awsebs.StorageClassName(request.Namespace, request.Spec.ClaimRef.Name, request.Name)
}

// eventsOf returns the events with reason on request, also after the
// request is gone. request must hold the UID of its create.
func eventsOf(g Gomega, request *pluginsv1alpha1.PluginRequest, reason string) []eventsv1.Event {
	var list eventsv1.EventList
	g.Expect(k8s.List(ctx, &list, client.InNamespace(request.Namespace))).To(Succeed())

	var found []eventsv1.Event
	for _, e := range list.Items {
		if e.Regarding.UID == request.UID && e.Reason == reason {
			found = append(found, e)
		}
	}
	return found
}
