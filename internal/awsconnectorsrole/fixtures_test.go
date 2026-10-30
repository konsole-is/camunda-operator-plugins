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

package awsconnectorsrole_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsconnectorsrole"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

const (
	timeout         = 30 * time.Second
	pluginNamespace = "camunda-operator-plugins"
	sectionName     = "connectors-permissions"
	rolePrefix      = "camunda-connectors"
	boundary        = "arn:aws:iam::111122223333:policy/camunda-boundary"
	providerARN     = "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC"
	issuer          = "oidc.eks.eu-west-1.amazonaws.com/id/ABC"
	eksCluster      = "eks-1"
	policyA         = "arn:aws:iam::111122223333:policy/connectors-a"
	policyB         = "arn:aws:iam::111122223333:policy/connectors-b"
	policyC         = "arn:aws:iam::111122223333:policy/connectors-c"
	existingRoleARN = "arn:aws:iam::111122223333:role/connectors"
)

// connectorsSection is the section as the operator ships it.
func connectorsSection() *pluginsv1alpha1.PluginSection {
	return &pluginsv1alpha1.PluginSection{
		ObjectMeta: metav1.ObjectMeta{Name: sectionName},
		Spec: pluginsv1alpha1.PluginSectionSpec{
			Label:            "Connectors permissions",
			Order:            70,
			Selection:        pluginsv1alpha1.SelectionOne,
			EmptyOptionLabel: "No cloud permissions",
			Slot:             pluginsv1alpha1.SlotConnectorsIdentity,
			AppliesTo:        []pluginsv1alpha1.ClaimKind{pluginsv1alpha1.ClaimKindCamundaClusterClaim},
			Outputs:          []pluginsv1alpha1.OutputDecl{{Key: "principal"}, {Key: "serviceAccountAnnotations"}},
		},
	}
}

// chartRegistrations returns the registrations of the values of the chart,
// with the defaults that its helper adds, so that the suite serves the
// schemas that the chart ships.
func chartRegistrations() []pluginsv1alpha1.PluginRegistration {
	values, err := os.ReadFile(filepath.Join("..", "..", "charts", "aws-connectors-role", "values.yaml"))
	Expect(err).NotTo(HaveOccurred())

	var chart struct {
		Registrations []map[string]any `json:"registrations"`
	}
	Expect(yaml.Unmarshal(values, &chart)).To(Succeed())

	for _, r := range chart.Registrations {
		r["sections"] = []string{sectionName}
		r["mode"] = pluginsv1alpha1.ModeOptional
		r["deletionPolicies"] = []string{"Delete", "Orphan"}
		r["outputs"] = []map[string]string{
			{"key": "principal", "format": "aws-iam-role-arn"},
			{"key": "serviceAccountAnnotations"},
		}
		if r["name"] == awsconnectorsrole.RegistrationRole {
			r["limits"] = []map[string]string{{"label": "Permissions boundary", "value": boundary}}
		}
	}

	out, err := yaml.Marshal(chart.Registrations)
	Expect(err).NotTo(HaveOccurred())
	path := filepath.Join(GinkgoT().TempDir(), "registrations.yaml")
	Expect(os.WriteFile(path, out, 0o600)).To(Succeed())

	registrations, err := pluginkit.LoadRegistrations(path)
	Expect(err).NotTo(HaveOccurred())
	return registrations
}

// installRBAC gives the ServiceAccount sa the roles of the chart, for the
// conformance check of the serve verb.
func installRBAC(
	ctx context.Context,
	c client.Client,
	sa pluginsv1alpha1.ServiceAccountRef,
	registration string,
) error {
	plugins := pluginsv1alpha1.GroupVersion.Group
	roles := map[string][]rbacv1.PolicyRule{
		sa.Name + "-serve": {
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"},
				ResourceNames: []string{registration}, Verbs: []string{"serve"}},
		},
		sa.Name + "-controller": {
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"},
				Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations"},
				ResourceNames: []string{registration}, Verbs: []string{"create", "patch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginregistrations/status"},
				ResourceNames: []string{registration}, Verbs: []string{"update"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginrequests"},
				Verbs: []string{"get", "list", "watch", "update", "patch"}},
			{APIGroups: []string{plugins}, Resources: []string{"pluginrequests/status", "pluginrequests/finalizers"},
				Verbs: []string{"update"}},
			{APIGroups: []string{plugins}, Resources: []string{"grants"},
				Verbs: []string{"get", "list", "watch", "delete"}},
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

var namespaceSeq atomic.Int32

// newNamespace creates a namespace of its own for one spec in the cluster
// of c.
func newNamespace(c *connectors) string {
	name := fmt.Sprintf("team-%d", namespaceSeq.Add(1))
	Expect(c.k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func raw(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

// connectorsRequest is the request of c of the claim, orders when none is
// given, for the cluster of the same name, with the deletion policy Delete.
// Its target names the cluster ServiceAccount as an additional one, as the
// role must never trust it.
func connectorsRequest(c *connectors, namespace, params string, claim ...string) *pluginsv1alpha1.PluginRequest {
	name := "orders"
	if len(claim) > 0 {
		name = claim[0]
	}
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name + "-connectors-permissions",
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      sectionName,
				pluginsv1alpha1.LabelRegistration: c.registration,
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:      sectionName,
			Registration: c.registration,
			ClaimRef:     pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: name},
			Target: pluginsv1alpha1.TargetRef{
				Kind:                      "CamundaCluster",
				Name:                      name,
				ServiceAccountName:        name + "-connectors",
				AdditionalServiceAccounts: []string{name + "-camunda"},
			},
			Parameters:     raw(params),
			DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
		},
	}
}

func read(g Gomega, c *connectors, request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.PluginRequest {
	got := &pluginsv1alpha1.PluginRequest{}
	g.Expect(c.mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), got)).To(Succeed())
	return got
}

func conditionOf(conditions []metav1.Condition, conditionType string) metav1.Condition {
	if cond := meta.FindStatusCondition(conditions, conditionType); cond != nil {
		return *cond
	}
	return metav1.Condition{}
}

// waitReady waits until request of c is Ready for its generation, and
// returns it.
func waitReady(c *connectors, request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.PluginRequest {
	GinkgoHelper()
	var got *pluginsv1alpha1.PluginRequest
	Eventually(func(g Gomega) {
		got = read(g, c, request)
		ready := conditionOf(got.Status.Conditions, v1.ConditionReady)
		g.Expect(ready.Status).To(Equal(metav1.ConditionTrue), "%s: %s", ready.Reason, ready.Message)
		g.Expect(ready.ObservedGeneration).To(Equal(got.Generation))
	}, timeout).Should(Succeed())
	return got
}

// waitReason waits until the condition conditionType of request of c has
// reason, and returns its message.
func waitReason(c *connectors, request *pluginsv1alpha1.PluginRequest, conditionType, reason string) string {
	GinkgoHelper()
	var message string
	Eventually(func(g Gomega) {
		cond := conditionOf(read(g, c, request).Status.Conditions, conditionType)
		g.Expect(cond.Reason).To(Equal(reason), cond.Message)
		message = cond.Message
	}, timeout).Should(Succeed())
	return message
}

// update changes request of c with change until the write goes through.
func update(c *connectors, request *pluginsv1alpha1.PluginRequest, change func(*pluginsv1alpha1.PluginRequest)) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		got := read(g, c, request)
		change(got)
		g.Expect(c.k8s.Update(ctx, got)).To(Succeed())
	}, timeout).Should(Succeed())
}

// waitGone deletes request of c and waits until it is gone.
func waitGone(c *connectors, request *pluginsv1alpha1.PluginRequest) {
	GinkgoHelper()
	Expect(c.k8s.Delete(ctx, request)).To(Succeed())
	Eventually(func(g Gomega) {
		err := c.mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(request), &pluginsv1alpha1.PluginRequest{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the request is gone: %v", err)
	}, timeout).Should(Succeed())
}

// rolesOf returns the roles of c whose namespace tag is namespace.
func rolesOf(c *connectors, namespace string) []string {
	var names []string
	for _, r := range c.iam.Roles() {
		if r.Tags[awskit.TagNamespace] == namespace {
			names = append(names, r.Name)
		}
	}
	return names
}

// associationsIn returns the associations of c in namespace.
func associationsIn(c *connectors, namespace string) []string {
	var sas []string
	for _, a := range c.eks.Associations() {
		if a.Namespace == namespace {
			sas = append(sas, a.ServiceAccount+"="+a.RoleARN)
		}
	}
	return sas
}
