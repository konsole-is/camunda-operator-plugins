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
	"fmt"
	"sync/atomic"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"
)

const (
	pluginNamespace = "camunda-operator-plugins"
	sectionName     = "cloud-identity"
	boundary        = "arn:aws:iam::111122223333:policy/camunda-boundary"
	providerARN     = "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC"
	issuer          = "oidc.eks.eu-west-1.amazonaws.com/id/ABC"
	eksCluster      = "eks-1"
)

// hubRegistration is a registration of the hub, as the chart writes it.
func hubRegistration(name string) pluginsv1alpha1.PluginRegistration {
	return pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: pluginsv1alpha1.PluginRegistrationSpec{
			Sections: []string{sectionName},
			Label:    "AWS IAM role",
			Mode:     pluginsv1alpha1.ModeOptional,
			DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{
				pluginsv1alpha1.DeletionPolicyDelete,
				pluginsv1alpha1.DeletionPolicyOrphan,
			},
			Outputs: []pluginsv1alpha1.OutputDecl{
				{Key: "principal", Format: "aws-iam-role-arn"},
				{Key: "serviceAccountAnnotations"},
			},
		},
	}
}

func cloudIdentitySection() *pluginsv1alpha1.PluginSection {
	return &pluginsv1alpha1.PluginSection{
		ObjectMeta: metav1.ObjectMeta{Name: sectionName},
		Spec: pluginsv1alpha1.PluginSectionSpec{
			Label:            "Cloud identity",
			Order:            10,
			Selection:        pluginsv1alpha1.SelectionOne,
			EmptyOptionLabel: "No cloud identity",
			Slot:             pluginsv1alpha1.SlotWorkloadIdentity,
			AppliesTo:        []pluginsv1alpha1.ClaimKind{pluginsv1alpha1.ClaimKindCamundaClusterClaim},
			Outputs:          []pluginsv1alpha1.OutputDecl{{Key: "principal"}, {Key: "serviceAccountAnnotations"}},
		},
	}
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
				Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{
				APIGroups: []string{plugins},
				Resources: []string{"grants/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
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

// newNamespace creates a namespace of its own for one spec.
func newNamespace() string {
	name := fmt.Sprintf("team-%d", namespaceSeq.Add(1))
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

func raw(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

// hubRequest is the cloud identity request of the claim orders, for the
// cluster of the same name, with the deletion policy Delete.
func hubRequest(registration, namespace string, additional ...string) *pluginsv1alpha1.PluginRequest {
	const claim = "orders"
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      claim + "-identity",
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      sectionName,
				pluginsv1alpha1.LabelRegistration: registration,
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:      sectionName,
			Registration: registration,
			ClaimRef:     pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: claim},
			Target: pluginsv1alpha1.TargetRef{
				Kind:                      "CamundaCluster",
				Name:                      claim,
				ServiceAccountName:        claim + "-camunda",
				AdditionalServiceAccounts: additional,
			},
			DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
		},
	}
}

// otherRegistration is the registration of the plugin that asks for the
// Grants of the specs in namespace. No controller serves it.
func otherRegistration(namespace string) string {
	return "s3-" + namespace
}

// otherRequest is a request of otherRegistration of the claim for the
// cluster, named name. The specs write its status, because no controller
// serves it.
func otherRequest(namespace, name, claim, cluster string) *pluginsv1alpha1.PluginRequest {
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      "backup-storage",
				pluginsv1alpha1.LabelRegistration: otherRegistration(namespace),
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:        "backup-storage",
			Registration:   otherRegistration(namespace),
			ClaimRef:       pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: claim},
			Target:         pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: cluster},
			DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
		},
	}
}

// otherRegistrationWith is the otherRegistration of namespace with one grant
// template.
func otherRegistrationWith(namespace, template, statement string) *pluginsv1alpha1.PluginRegistration {
	return &pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: otherRegistration(namespace)},
		Spec: pluginsv1alpha1.PluginRegistrationSpec{
			Sections:         []string{"backup-storage"},
			Label:            "Test bucket",
			Mode:             pluginsv1alpha1.ModeOptional,
			ServiceAccount:   pluginsv1alpha1.ServiceAccountRef{Namespace: pluginNamespace, Name: "s3-test"},
			DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{pluginsv1alpha1.DeletionPolicyDelete},
			GrantTemplates: []pluginsv1alpha1.GrantTemplate{{
				Name:     template,
				Format:   pluginsv1alpha1.GrantFormatAWSIAMStatement,
				Template: raw(statement),
			}},
		},
	}
}

// grantFor is a Grant of request for its cluster and the template read, as
// the plugin of request writes it.
func grantFor(request *pluginsv1alpha1.PluginRequest, name, fragment string) *pluginsv1alpha1.Grant {
	return &pluginsv1alpha1.Grant{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: request.Namespace,
			Name:      name,
			Labels: map[string]string{
				pluginsv1alpha1.LabelRegistration: request.Spec.Registration,
				pluginsv1alpha1.LabelRequest:      request.Name,
				pluginsv1alpha1.LabelCluster:      request.Spec.Target.Name,
			},
		},
		Spec: pluginsv1alpha1.GrantSpec{
			Cluster:  v1.ClusterRef{Name: request.Spec.Target.Name},
			Template: "read",
			Format:   pluginsv1alpha1.GrantFormatAWSIAMStatement,
			Fragment: raw(fragment),
		},
	}
}
