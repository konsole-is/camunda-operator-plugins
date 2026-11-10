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
	registration string
	prefix       string
	// scope is the EKS cluster identity that role names hash.
	scope          string
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

// roleName is the name of the role of the claim orders in namespace.
func (h *hub) roleName(namespace string) string {
	return awskit.RoleName(h.prefix, h.scope, namespace, "orders")
}

// newHub returns a manager of mode that serves the registration name, with
// the prefix camunda and fakes in which the boundary exists.
func newHub(mode awskit.Mode, registration string) *hub {
	return newHubWith(mode, registration, "camunda", awsfake.NewIAM(), awsfake.NewEKS())
}

// newHubWith returns a manager of mode that serves the registration name
// with the role name prefix, on the fakes iam and eks. It adds the boundary
// to iam.
func newHubWith(mode awskit.Mode, registration, prefix string, iam *awsfake.IAM, eks *awsfake.EKS) *hub {
	h := &hub{
		registration:   registration,
		prefix:         prefix,
		serviceAccount: pluginsv1alpha1.ServiceAccountRef{Namespace: pluginNamespace, Name: registration},
		scope:          issuer,
		iam:            iam,
		eks:            eks,
	}
	h.iam.AddPolicy(boundary)
	if mode == awskit.ModePodIdentity {
		h.scope = eksCluster
	}

	var err error
	h.mgr, err = awsidentity.NewManager(restConfig, awsidentity.Options{
		// Controller names are unique in the process.
		Name:                   "aws-identity-" + registration + "-" + prefix,
		HealthProbeAddress:     "0",
		MetricsAddress:         "0",
		Registrations:          []pluginsv1alpha1.PluginRegistration{hubRegistration(registration)},
		ServiceAccount:         h.serviceAccount,
		Mode:                   mode,
		PermissionsBoundaryARN: boundary,
		RoleNamePrefix:         prefix,
		ExtraTags:              awskit.ExtraTags{"cost-center": "camunda"},
		AllowedActions:         []string{"s3:*"},
		OIDCProviderARN:        providerARN,
		EKSClusterName:         eksCluster,
		Clients:                awskit.Clients{IAM: h.iam, EKS: h.eks, STS: awsfake.NewSTS()},
	})
	Expect(err).NotTo(HaveOccurred())

	return h
}

// startHub starts a hub that a spec stops again, as an admin who changes a
// chart value restarts the controller. stop returns after the manager ended.
func startHub(mode awskit.Mode, registration, prefix string, iam *awsfake.IAM, eks *awsfake.EKS) (*hub, func()) {
	h := newHubWith(mode, registration, prefix, iam, eks)
	// The hubs of one registration share its ServiceAccount and its RBAC.
	Expect(client.IgnoreAlreadyExists(installRBAC(ctx, k8s, h.serviceAccount, registration))).To(Succeed())

	hubCtx, stopHub := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(done)
		Expect(h.mgr.Start(hubCtx)).To(Succeed())
	}()

	return h, func() {
		stopHub()
		<-done
	}
}
