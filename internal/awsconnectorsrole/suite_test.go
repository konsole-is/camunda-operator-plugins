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
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/awsconnectorsrole"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

// cluster is one envtest API server. A registration name is cluster-scoped,
// and the plugin serves the names of the chart, so the IRSA managers and the
// Pod Identity managers each get a cluster of their own.
type cluster struct {
	env        *envtest.Environment
	restConfig *rest.Config
	k8s        client.Client
}

// connectors is one manager of the suite, with its AWS fakes.
type connectors struct {
	*cluster
	registration string
	mode         awskit.Mode
	// scope is the EKS cluster identity that role names hash.
	scope          string
	serviceAccount pluginsv1alpha1.ServiceAccountRef
	mgr            ctrl.Manager
	iam            *awsfake.IAM
	eks            *awsfake.EKS
}

var (
	ctx    context.Context
	cancel context.CancelFunc
	// irsaCluster holds role and existing, podIdentityCluster the other two.
	irsaCluster, podIdentityCluster *cluster
	// Each manager serves one registration in one mode, with fakes of its
	// own.
	role, rolePodIdentity, existing, existingPodIdentity *connectors
)

func TestAWSConnectorsRole(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AWS Connectors Role Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	crds := os.Getenv("ENVTEST_CRDS")
	Expect(crds).NotTo(BeEmpty(), "ENVTEST_CRDS is not set: run make test, which downloads the CRD bases")

	ctx, cancel = context.WithCancel(context.Background())
	irsaCluster = startCluster(crds)
	podIdentityCluster = startCluster(crds)

	registrations := chartRegistrations()
	role = newConnectors(irsaCluster, registrations, awsconnectorsrole.RegistrationRole, awskit.ModeIRSA)
	existing = newConnectors(irsaCluster, registrations, awsconnectorsrole.RegistrationExistingRole, awskit.ModeIRSA)
	rolePodIdentity = newConnectors(
		podIdentityCluster,
		registrations,
		awsconnectorsrole.RegistrationRole,
		awskit.ModePodIdentity,
	)
	existingPodIdentity = newConnectors(
		podIdentityCluster,
		registrations,
		awsconnectorsrole.RegistrationExistingRole,
		awskit.ModePodIdentity,
	)

	for _, c := range []*connectors{role, existing, rolePodIdentity, existingPodIdentity} {
		Expect(installRBAC(ctx, c.k8s, c.serviceAccount, c.registration)).To(Succeed())
		go func(m ctrl.Manager) {
			defer GinkgoRecover()
			Expect(m.Start(ctx)).To(Succeed())
		}(c.mgr)
	}
})

var _ = AfterSuite(func() {
	cancel()
	for _, c := range []*cluster{irsaCluster, podIdentityCluster} {
		Expect(c.env.Stop()).To(Succeed())
	}
})

// startCluster starts an API server with the CRDs of crds, the namespace of
// the plugin, and the connectors-permissions section.
func startCluster(crds string) *cluster {
	c := &cluster{env: &envtest.Environment{CRDDirectoryPaths: []string{crds}, ErrorIfCRDPathMissing: true}}
	var err error
	c.restConfig, err = c.env.Start()
	Expect(err).NotTo(HaveOccurred())

	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(pluginsv1alpha1.AddToScheme(scheme)).To(Succeed())
	c.k8s, err = client.New(c.restConfig, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	Expect(c.k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: pluginNamespace}})).To(Succeed())
	Expect(c.k8s.Create(ctx, connectorsSection())).To(Succeed())
	return c
}

// newConnectors returns a manager of mode in c that serves the registration
// of the chart named registration. The fakes of a manager of
// RegistrationRole hold the boundary and the test policies.
func newConnectors(
	c *cluster,
	registrations []pluginsv1alpha1.PluginRegistration,
	registration string,
	mode awskit.Mode,
) *connectors {
	h := &connectors{
		cluster:        c,
		registration:   registration,
		mode:           mode,
		scope:          issuer,
		serviceAccount: pluginsv1alpha1.ServiceAccountRef{Namespace: pluginNamespace, Name: registration},
		iam:            awsfake.NewIAM(),
		eks:            awsfake.NewEKS(),
	}
	if mode == awskit.ModePodIdentity {
		h.scope = eksCluster
	}

	opts := awsconnectorsrole.Options{
		Name:               registration + "-" + string(mode),
		HealthProbeAddress: "0",
		MetricsAddress:     "0",
		ServiceAccount:     h.serviceAccount,
		Mode:               mode,
		ExtraTags:          awskit.ExtraTags{"cost-center": "camunda"},
		OIDCProviderARN:    providerARN,
		EKSClusterName:     eksCluster,
		Clients:            awskit.Clients{IAM: h.iam, EKS: h.eks, STS: awsfake.NewSTS()},
	}
	if registration == awsconnectorsrole.RegistrationRole {
		opts.RoleNamePrefix = rolePrefix
		opts.PermissionsBoundaryARN = boundary
		for _, p := range []string{boundary, policyA, policyB, policyC} {
			h.iam.AddPolicy(p)
		}
	}
	for _, r := range registrations {
		if r.Name == registration {
			opts.Registrations = []pluginsv1alpha1.PluginRegistration{r}
		}
	}
	Expect(opts.Registrations).To(HaveLen(1), "the chart has the registration %s", registration)

	var err error
	h.mgr, err = awsconnectorsrole.NewManager(c.restConfig, opts)
	Expect(err).NotTo(HaveOccurred())

	return h
}

// roleName is the name of the role of the claim orders in namespace.
func (c *connectors) roleName(namespace string) string {
	return awskit.RoleName(rolePrefix, c.scope, namespace, "orders")
}
