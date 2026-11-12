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

package awss3

import (
	"cmp"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// name is the default controller name of the plugin.
const name = "aws-s3"

// Options are the settings of the manager that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-s3. Controller names are
	// unique in a process, so two managers in one process need two names.
	Name string
	// HealthProbeAddress is the address of /healthz and /readyz, for example
	// ":8081". "0" turns the probes off.
	HealthProbeAddress string
	// MetricsAddress is the address of /metrics. "0" turns the metrics off.
	MetricsAddress string
	// Registrations are the registrations that the plugin publishes and
	// serves. NewManager refuses an empty list.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// LeaderElection makes only the replica that holds the Lease
	// LeaderElectionID write. It needs a LeaderElectionID.
	LeaderElection   bool
	LeaderElectionID string
	// Settings are the settings of the buckets.
	Settings Settings
	// Resync is the pluginkit.Config.Resync of the plugin.
	Resync time.Duration
	// SettleTime is how long S3 can show a bucket setting as it was before a
	// write of the plugin. Zero is five minutes.
	SettleTime time.Duration
	// S3 returns the S3 client of a region.
	S3 func(region string) awskit.S3
	// CloudWatch returns the CloudWatch client of a region.
	CloudWatch func(region string) awskit.CloudWatch
}

// NewManager returns a manager for cfg that publishes and serves the
// registrations of opts. It refuses settings that fail Settings.Validate and
// a nil client function. /healthz and /readyz answer while it runs.
func NewManager(cfg *rest.Config, opts Options) (ctrl.Manager, error) {
	if err := opts.Settings.Validate(); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}

	if opts.S3 == nil || opts.CloudWatch == nil {
		return nil, errors.New("the S3 or the CloudWatch client is missing")
	}

	scheme := runtime.NewScheme()
	for _, add := range []struct {
		what    string
		install func(*runtime.Scheme) error
	}{
		{"the Kubernetes kinds", clientgoscheme.AddToScheme},
		{"the plugins.camunda.io kinds", pluginsv1alpha1.AddToScheme},
		{"the core.camunda.io kinds", v1.AddToScheme},
	} {
		if err := add.install(scheme); err != nil {
			return nil, fmt.Errorf("adding %s to the scheme: %w", add.what, err)
		}
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: opts.HealthProbeAddress,
		Metrics:                metricsserver.Options{BindAddress: opts.MetricsAddress},
		LeaderElection:         opts.LeaderElection,
		LeaderElectionID:       opts.LeaderElectionID,
	})
	if err != nil {
		return nil, fmt.Errorf("creating the manager: %w", err)
	}

	plugin := &Plugin{
		settings: opts.Settings,
		buckets: buckets{
			s3:           opts.S3,
			deleteBudget: opts.Settings.DeleteBudget,
			writes:       newWriteTimes(cmp.Or(opts.SettleTime, settleTime), time.Now),
		},
		cloudWatch: opts.CloudWatch,
		reader:     mgr.GetAPIReader(),
		writer:     mgr.GetClient(),
	}

	controller := opts.Name
	if controller == "" {
		controller = name
	}
	if err := pluginkit.Setup(mgr, pluginkit.Config{
		Name:           controller,
		Version:        version.Version(),
		Registrations:  opts.Registrations,
		ServiceAccount: opts.ServiceAccount,
		Owns:           []client.Object{&v1.ObjectStorageConfig{}},
		Health:         plugin.health,
		Resync:         opts.Resync,
	}, plugin); err != nil {
		return nil, fmt.Errorf("setting up the plugin: %w", err)
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding the health check: %w", err)
	}

	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding the ready check: %w", err)
	}

	return mgr, nil
}
