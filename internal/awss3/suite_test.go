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
	"os"
	"testing"
	"time"

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

	"github.com/konsole-is/camunda-operator-plugins/internal/awss3"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

var (
	ctx        context.Context
	cancel     context.CancelFunc
	testEnv    *envtest.Environment
	restConfig *rest.Config
	k8s        client.Client
	// mgr is the manager that runs now. A spec that restarts the plugin
	// replaces it.
	mgr     ctrl.Manager
	stopMgr func()
	// fake is the S3 of every region, and cloudWatch the CloudWatch.
	fake       *awsfake.S3
	cloudWatch *awsfake.CloudWatch
	options    awss3.Options
)

func TestAWSS3(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AWS S3 Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	crds := os.Getenv("ENVTEST_CRDS")
	Expect(crds).NotTo(BeEmpty(), "ENVTEST_CRDS is not set: run make test, which downloads the CRD bases")

	testEnv = &envtest.Environment{CRDDirectoryPaths: []string{crds}, ErrorIfCRDPathMissing: true}
	var err error
	restConfig, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	registrations, err := chartRegistrations(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())

	fake = awsfake.NewS3()
	cloudWatch = awsfake.NewCloudWatch()
	sa := pluginsv1alpha1.ServiceAccountRef{Namespace: pluginNamespace, Name: "aws-s3"}
	options = awss3.Options{
		HealthProbeAddress: "0",
		MetricsAddress:     "0",
		Registrations:      registrations,
		ServiceAccount:     sa,
		Settings: awss3.Settings{
			Region:    home,
			Partition: "aws",
			Mode:      awskit.ModeIRSA,
			ExtraTags: awskit.ExtraTags{"team": "platform"},
			// One pass deletes one batch of objects, so a bucket of more
			// than 1,000 objects takes more than one pass.
			DeleteBudget: time.Nanosecond,
		},
		// Long enough for the spec of a setting that S3 shows late, short
		// enough for the specs of drift.
		SettleTime: 15 * time.Second,
		S3:         fake.In,
		CloudWatch: cloudWatch.In,
	}

	ctx, cancel = context.WithCancel(context.Background())
	startManager("aws-s3")

	k8s, err = client.New(restConfig, client.Options{Scheme: mgr.GetScheme()})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: pluginNamespace}})).To(Succeed())
	for _, section := range sections() {
		Expect(k8s.Create(ctx, section)).To(Succeed())
	}
	Expect(installRBAC(ctx, k8s, sa, registration)).To(Succeed())
})

var _ = AfterSuite(func() {
	stopMgr()
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})

// startManager starts a manager of the plugin with the controller name
// name, as a new process of the plugin does, and sets mgr and stopMgr.
// Controller names are unique in a process, so each start needs a new name.
func startManager(name string) {
	GinkgoHelper()
	opts := options
	opts.Name = name
	m, err := awss3.NewManager(restConfig, opts)
	Expect(err).NotTo(HaveOccurred())

	mgrCtx, mgrCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		defer close(done)
		Expect(m.Start(mgrCtx)).To(Succeed())
	}()

	mgr = m
	stopMgr = func() {
		mgrCancel()
		<-done
	}
}
