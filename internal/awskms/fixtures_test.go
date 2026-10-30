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

package awskms_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

const (
	pluginNamespace = "camunda-operator-plugins"
	sectionName     = "encryption-key"
	singleRegion    = "aws-kms-key"
	multiRegion     = "aws-kms-key-multi-region"
	home            = "eu-west-1"
	replica         = "eu-central-1"
)

// The parameter schemas of the registrations, as the chart writes them.
const (
	singleRegionParameters = `{"type":"object","properties":{` +
		`"pendingWindowDays":{"type":"integer","minimum":7,"maximum":30,"default":30}}}`
	multiRegionParameters = `{"type":"object","required":["replicaRegions"],` +
		`"properties":{"pendingWindowDays":{"type":"integer","minimum":7,"maximum":30,"default":30},` +
		`"replicaRegions":{"type":"array","minItems":1,"x-kubernetes-list-type":"set",` +
		`"items":{"type":"string","pattern":"^([a-z]+-){2,3}[0-9]+$"}}}}`
)

// kmsRegistration is a registration of the plugin, as the chart writes it.
func kmsRegistration(name string, multi bool) pluginsv1alpha1.PluginRegistration {
	label, parameters := "AWS KMS key", singleRegionParameters
	if multi {
		label, parameters = "AWS KMS multi-Region key", multiRegionParameters
	}

	return pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: pluginsv1alpha1.PluginRegistrationSpec{
			Sections: []string{sectionName},
			Label:    label,
			Mode:     pluginsv1alpha1.ModeOptional,
			DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{
				pluginsv1alpha1.DeletionPolicyDelete,
				pluginsv1alpha1.DeletionPolicyOrphan,
			},
			Parameters: raw(parameters),
			Outputs: []pluginsv1alpha1.OutputDecl{
				{Key: "keyRef", Format: "aws-kms-arn"},
				{Key: "format"},
			},
		},
	}
}

func encryptionKeySection() *pluginsv1alpha1.PluginSection {
	return &pluginsv1alpha1.PluginSection{
		ObjectMeta: metav1.ObjectMeta{Name: sectionName},
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

// installRBAC gives the ServiceAccount sa the roles of the chart, for the
// conformance check of the serve verb.
func installRBAC(
	ctx context.Context,
	c client.Client,
	sa pluginsv1alpha1.ServiceAccountRef,
	registrations ...string,
) error {
	plugins := pluginsv1alpha1.GroupVersion.Group
	roles := map[string][]rbacv1.PolicyRule{
		sa.Name + "-serve": {
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"},
				ResourceNames: registrations, Verbs: []string{"serve"}},
		},
		sa.Name + "-controller": {
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"},
				Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"},
				ResourceNames: registrations, Verbs: []string{"create", "patch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations/status"},
				ResourceNames: registrations, Verbs: []string{"update"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginrequests"},
				Verbs: []string{"get", "list", "watch", "update", "patch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginrequests/status", "pluginrequests/finalizers"},
				Verbs: []string{"update"}},
			{APIGroups: []string{plugins}, Resources: []string{"grants"},
				Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{APIGroups: []string{"events.k8s.io"}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
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

// kmsRequest is the encryption key request of the claim orders, for the
// cluster of the same name, with params and policy.
func kmsRequest(
	registration, namespace, params string,
	policy pluginsv1alpha1.DeletionPolicy,
) *pluginsv1alpha1.PluginRequest {
	const claim = "orders"
	request := &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      claim + "-encryption-key",
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      sectionName,
				pluginsv1alpha1.LabelRegistration: registration,
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:        sectionName,
			Registration:   registration,
			ClaimRef:       pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: claim},
			Target:         pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: claim},
			DeletionPolicy: policy,
		},
	}
	if params != "" {
		request.Spec.Parameters = raw(params)
	}
	return request
}
