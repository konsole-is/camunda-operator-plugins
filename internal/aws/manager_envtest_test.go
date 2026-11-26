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

package aws_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/aws"
	"github.com/konsole-is/camunda-operator-plugins/internal/awsconnectorsrole"
	"github.com/konsole-is/camunda-operator-plugins/internal/awsebs"
	"github.com/konsole-is/camunda-operator-plugins/internal/awsidentity"
	"github.com/konsole-is/camunda-operator-plugins/internal/awskms"
	"github.com/konsole-is/camunda-operator-plugins/internal/awss3"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

const (
	boundary    = "arn:aws:iam::111122223333:policy/camunda-boundary"
	providerARN = "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/ABC"
)

// serviceAccount is the one ServiceAccount of every controller of the manager.
var serviceAccount = pluginsv1alpha1.ServiceAccountRef{Namespace: pluginNamespace, Name: "aws"}

var _ = Describe("one manager", Ordered, func() {
	It("publishes the registrations of every controller, each under its own controller name", func() {
		stop := start(everyController())
		DeferCleanup(stop)

		for name, controller := range map[string]string{
			"aws-irsa":                     "aws-identity",
			"aws-connectors-role":          "aws-connectors-role",
			"aws-connectors-existing-role": "aws-connectors-role",
			"aws-kms-key":                  "aws-kms",
			"aws-kms-key-multi-region":     "aws-kms",
			"aws-ebs-encrypted":            "aws-ebs",
			"aws-s3":                       "aws-s3",
		} {
			Eventually(func(g Gomega) {
				var r pluginsv1alpha1.PluginRegistration
				g.Expect(k8s.Get(ctx, client.ObjectKey{Name: name}, &r)).To(Succeed())
				g.Expect(r.Spec.Controller.Name).To(Equal(controller))
				g.Expect(r.Spec.ServiceAccount).To(Equal(serviceAccount))
			}).Should(Succeed(), name)
		}
	})

	It("publishes no registration and serves no request of a controller that is off", func() {
		// The spec above left these registrations. Without them, the
		// registration of aws-ebs proves that the new manager runs, and the
		// absence of aws-kms-key is not a leftover.
		for _, name := range []string{"aws-ebs-encrypted", "aws-kms-key"} {
			deleteRegistration(name)
		}

		stop := start(aws.Options{HealthProbeAddress: "0", MetricsAddress: "0", EBS: ebsOptions("aws-ebs-alone")})
		DeferCleanup(stop)

		Eventually(func(g Gomega) {
			var r pluginsv1alpha1.PluginRegistration
			g.Expect(k8s.Get(ctx, client.ObjectKey{Name: "aws-ebs-encrypted"}, &r)).To(Succeed())
			g.Expect(r.Spec.Controller.Name).To(Equal("aws-ebs-alone"))
		}).Should(Succeed())

		request := kmsRequest(newNamespace())
		Expect(k8s.Create(ctx, request)).To(Succeed())
		Consistently(func(g Gomega) {
			err := k8s.Get(ctx, client.ObjectKey{Name: "aws-kms-key"}, &pluginsv1alpha1.PluginRegistration{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "aws-kms-key is not published: %v", err)

			var got pluginsv1alpha1.PluginRequest
			g.Expect(k8s.Get(ctx, client.ObjectKeyFromObject(request), &got)).To(Succeed())
			g.Expect(got.Finalizers).NotTo(ContainElement(pluginsv1alpha1.FinalizerCleanup))
		}, 10*time.Second).Should(Succeed())
	})
})

// start starts a manager of opts. stop returns after the manager ended.
func start(opts aws.Options) (stop func()) {
	GinkgoHelper()
	mgr, err := aws.NewManager(restConfig, opts)
	Expect(err).NotTo(HaveOccurred())

	mgrCtx, mgrCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(done)
		Expect(mgr.Start(mgrCtx)).To(Succeed())
	}()

	return func() {
		mgrCancel()
		<-done
	}
}

// everyController turns on each controller with its default controller name.
func everyController() aws.Options {
	iam := awsfake.NewIAM()
	iam.AddPolicy(boundary)
	clients := awskit.Clients{IAM: iam, STS: awsfake.NewSTS()}

	return aws.Options{
		HealthProbeAddress: "0",
		MetricsAddress:     "0",
		Identity: &awsidentity.Options{
			Registrations:          []pluginsv1alpha1.PluginRegistration{registration("aws-irsa", "cloud-identity")},
			ServiceAccount:         serviceAccount,
			Mode:                   awskit.ModeIRSA,
			PermissionsBoundaryARN: boundary,
			RoleNamePrefix:         "camunda",
			AllowedActions:         []string{"s3:*"},
			OIDCProviderARN:        providerARN,
			Clients:                clients,
		},
		ConnectorsRole: &awsconnectorsrole.Options{
			Registrations: []pluginsv1alpha1.PluginRegistration{
				registration(awsconnectorsrole.RegistrationRole, "connectors-permissions"),
				registration(awsconnectorsrole.RegistrationExistingRole, "connectors-permissions"),
			},
			ServiceAccount:         serviceAccount,
			Mode:                   awskit.ModeIRSA,
			RoleNamePrefix:         "camunda-connectors",
			PermissionsBoundaryARN: boundary,
			OIDCProviderARN:        providerARN,
			Clients:                clients,
		},
		KMS: &awskms.Options{
			Registrations: []pluginsv1alpha1.PluginRegistration{
				registration("aws-kms-key", "encryption-key"),
				registration("aws-kms-key-multi-region", "encryption-key"),
			},
			ServiceAccount: serviceAccount,
			Settings:       validKMSSettings(),
			KMS:            awsfake.NewKMS().In,
		},
		EBS: ebsOptions(""),
		S3: &awss3.Options{
			Registrations:  []pluginsv1alpha1.PluginRegistration{registration("aws-s3", "backup-storage")},
			ServiceAccount: serviceAccount,
			Settings: awss3.Settings{
				Region:       "eu-west-1",
				Partition:    "aws",
				Mode:         awskit.ModeIRSA,
				DeleteBudget: time.Second,
			},
			S3:         awsfake.NewS3().In,
			CloudWatch: awsfake.NewCloudWatch().In,
		},
	}
}

// ebsOptions turns on aws-ebs with the controller name name. Empty is the
// default name.
func ebsOptions(name string) *awsebs.Options {
	return &awsebs.Options{
		Name:           name,
		Registrations:  []pluginsv1alpha1.PluginRegistration{registration("aws-ebs-encrypted", "volume-encryption")},
		ServiceAccount: serviceAccount,
	}
}

func registration(name string, sections ...string) pluginsv1alpha1.PluginRegistration {
	return pluginsv1alpha1.PluginRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: pluginsv1alpha1.PluginRegistrationSpec{
			Sections: sections,
			Label:    name,
			Mode:     pluginsv1alpha1.ModeOptional,
			DeletionPolicies: []pluginsv1alpha1.DeletionPolicy{
				pluginsv1alpha1.DeletionPolicyDelete,
				pluginsv1alpha1.DeletionPolicyOrphan,
			},
		},
	}
}

// deleteRegistration deletes the registration name and waits until it is gone.
func deleteRegistration(name string) {
	GinkgoHelper()
	r := &pluginsv1alpha1.PluginRegistration{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(client.IgnoreNotFound(k8s.Delete(ctx, r))).To(Succeed())
	Eventually(func() bool {
		return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKeyFromObject(r), r))
	}).Should(BeTrue(), name)
}

// newNamespace creates a namespace of its own for one spec.
func newNamespace() string {
	GinkgoHelper()
	name := "team-" + rand.String(5)
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
	return name
}

// kmsRequest is a request of the registration aws-kms-key in namespace.
func kmsRequest(namespace string) *pluginsv1alpha1.PluginRequest {
	const claim, section, registrationName = "orders", "encryption-key", "aws-kms-key"
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      claim + "-" + section,
			Labels: map[string]string{
				pluginsv1alpha1.LabelSection:      section,
				pluginsv1alpha1.LabelRegistration: registrationName,
			},
		},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Section:        section,
			Registration:   registrationName,
			ClaimRef:       pluginsv1alpha1.ClaimRef{Kind: pluginsv1alpha1.ClaimKindCamundaClusterClaim, Name: claim},
			Target:         pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: claim},
			DeletionPolicy: pluginsv1alpha1.DeletionPolicyDelete,
		},
	}
}
