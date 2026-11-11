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

package awss3_test

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
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

const (
	pluginNamespace = "camunda-operator-plugins"
	registration    = "aws-s3"
	home            = "eu-west-1"
	keyARN          = "arn:aws:kms:eu-west-1:111122223333:key/0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0"
	roleARN         = "arn:aws:iam::111122223333:role/cpo-test"
	timeout         = 30 * time.Second
)

// chartRegistrations returns the registrations of the values of the chart,
// as the controller loads them from the ConfigMap of the chart, so that the
// specs read the real grant templates.
func chartRegistrations(dir string) ([]pluginsv1alpha1.PluginRegistration, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "charts", "aws-s3", "values.yaml"))
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

// sections are the core section of the plugin and the sections that its
// inputs read, as the operator writes them.
func sections() []*pluginsv1alpha1.PluginSection {
	clusterClaims := []pluginsv1alpha1.ClaimKind{pluginsv1alpha1.ClaimKindCamundaClusterClaim}
	return []*pluginsv1alpha1.PluginSection{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "backup-storage"},
			Spec: pluginsv1alpha1.PluginSectionSpec{
				Label:            "Backup storage",
				Order:            40,
				Selection:        pluginsv1alpha1.SelectionOne,
				EmptyOptionLabel: "No backups",
				Slot:             pluginsv1alpha1.SlotBackupStorage,
				AppliesTo:        clusterClaims,
				Outputs:          []pluginsv1alpha1.OutputDecl{{Key: "objectStorageConfig"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "encryption-key"},
			Spec: pluginsv1alpha1.PluginSectionSpec{
				Label:            "Encryption key",
				Order:            20,
				Selection:        pluginsv1alpha1.SelectionOne,
				EmptyOptionLabel: "No key",
				AppliesTo:        clusterClaims,
				Outputs:          []pluginsv1alpha1.OutputDecl{{Key: "keyRef"}, {Key: "format"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "cloud-identity"},
			Spec: pluginsv1alpha1.PluginSectionSpec{
				Label:            "Cloud identity",
				Order:            10,
				Selection:        pluginsv1alpha1.SelectionOne,
				EmptyOptionLabel: "No cloud identity",
				AppliesTo:        clusterClaims,
				Outputs:          []pluginsv1alpha1.OutputDecl{{Key: "principal"}},
			},
		},
	}
}

// installRBAC creates the ServiceAccount sa and the ClusterRoles of
// charts/aws-s3/templates/rbac.yaml for the registrations, bound to sa. The
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
			{APIGroups: []string{v1.GroupVersion.Group}, Resources: []string{"objectstorageconfigs"}, Verbs: write},
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

// bucketRequest is the backup storage request of the claim orders, for the
// cluster of the same name, with inputs and policy.
func bucketRequest(
	namespace string,
	inputs map[string]string,
	policy pluginsv1alpha1.DeletionPolicy,
) *pluginsv1alpha1.PluginRequest {
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "orders-backup-storage",
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      "backup-storage",
				pluginsv1alpha1.LabelRegistration: registration,
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:      "backup-storage",
			Registration: registration,
			ClaimRef:     pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: "orders"},
			Target: pluginsv1alpha1.TargetRef{
				Kind:               "CamundaCluster",
				Name:               "orders",
				ServiceAccountName: "orders-camunda",
			},
			Inputs:         inputs,
			DeletionPolicy: policy,
		},
	}
}
