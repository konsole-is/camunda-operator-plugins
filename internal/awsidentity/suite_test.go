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
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsidentity"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

// hub is one manager of the suite, with its AWS fakes.
type hub struct {
	registration   string
	serviceAccount pluginsv1alpha1.ServiceAccountRef
	mgr            ctrl.Manager
	iam            *awsfake.IAM
	eks            *awsfake.EKS
}

var (
	ctx        context.Context
	cancel     context.CancelFunc
	testEnv    *envtest.Environment
	restConfig *rest.Config
	k8s        client.Client
	// irsa serves aws-irsa and podIdentity serves aws-pod-identity, each with
	// fakes of its own.
	irsa, podIdentity *hub
)

func TestAWSIdentity(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AWS Identity Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	crds := os.Getenv("ENVTEST_CRDS")
	Expect(crds).NotTo(BeEmpty(), "ENVTEST_CRDS is not set: run make test, which downloads the CRD bases")

	testEnv = &envtest.Environment{CRDDirectoryPaths: []string{crds}, ErrorIfCRDPathMissing: true}
	var err error
	restConfig, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	irsa = newHub(awskit.ModeIRSA, "aws-irsa")
	podIdentity = newHub(awskit.ModePodIdentity, "aws-pod-identity")

	k8s, err = client.New(restConfig, client.Options{Scheme: irsa.mgr.GetScheme()})
	Expect(err).NotTo(HaveOccurred())

	ctx, cancel = context.WithCancel(context.Background())
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: pluginNamespace}})).To(Succeed())
	Expect(k8s.Create(ctx, cloudIdentitySection())).To(Succeed())
	for _, h := range []*hub{irsa, podIdentity} {
		Expect(installRBAC(ctx, k8s, h.serviceAccount, h.registration)).To(Succeed())
		go func(m ctrl.Manager) {
			defer GinkgoRecover()
			Expect(m.Start(ctx)).To(Succeed())
		}(h.mgr)
	}
})

var _ = AfterSuite(func() {
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})

// newHub returns a manager of mode that serves the registration name, with
// fakes in which the boundary exists.
func newHub(mode awskit.Mode, registration string) *hub {
	h := &hub{
		registration:   registration,
		serviceAccount: pluginsv1alpha1.ServiceAccountRef{Namespace: pluginNamespace, Name: registration},
		iam:            awsfake.NewIAM(),
		eks:            awsfake.NewEKS(),
	}
	h.iam.AddPolicy(boundary)

	var err error
	h.mgr, err = awsidentity.NewManager(restConfig, awsidentity.Options{
		Name:                   "aws-identity-" + string(mode),
		HealthProbeAddress:     "0",
		MetricsAddress:         "0",
		Registrations:          []pluginsv1alpha1.PluginRegistration{hubRegistration(registration)},
		ServiceAccount:         h.serviceAccount,
		Mode:                   mode,
		PermissionsBoundaryARN: boundary,
		AllowedActions:         []string{"s3:*"},
		OIDCProviderARN:        providerARN,
		EKSClusterName:         eksCluster,
		Clients:                awskit.Clients{IAM: h.iam, EKS: h.eks, STS: awsfake.NewSTS()},
	})
	Expect(err).NotTo(HaveOccurred())

	return h
}
