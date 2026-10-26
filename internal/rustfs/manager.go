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

// Package rustfs is the rustfs plugin. It serves the backup-storage section
// through pluginkit: it gives each cluster its own RustFS bucket and an access
// key that only that bucket accepts.
package rustfs

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// name is the controller name of the plugin.
const name = "rustfs"

// Options are the settings of the manager that the command line sets.
type Options struct {
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
	// Settings are the RustFS server of every request.
	Settings Settings
	// HealthInterval is the time between two checks that RustFS answers and
	// accepts the admin key. Zero is the default of pluginkit.
	HealthInterval time.Duration
}

// NewManager returns a manager for cfg that publishes and serves the
// registrations of opts. It refuses settings that are not valid. /healthz
// and /readyz answer while it runs.
func NewManager(cfg *rest.Config, opts Options) (ctrl.Manager, error) {
	s := opts.Settings
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("checking the RustFS settings: %w", err)
	}

	a, err := newAdminClient(s.Endpoint, s.region(), s.AdminAccessKey, s.AdminSecretKey)
	if err != nil {
		return nil, err
	}

	b := newBucketClient(s.Endpoint, s.region(), s.AdminAccessKey, s.AdminSecretKey)
	return newManager(cfg, opts, a, b)
}

// newManager is NewManager with the RustFS clients a and b.
func newManager(cfg *rest.Config, opts Options, a admin, b buckets) (ctrl.Manager, error) {
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
		admin:    a,
		buckets:  b,
		client:   mgr.GetClient(),
		reader:   mgr.GetAPIReader(),
	}
	if err := pluginkit.Setup(mgr, pluginkit.Config{
		Name:           name,
		Version:        version.Version(),
		Registrations:  opts.Registrations,
		ServiceAccount: opts.ServiceAccount,
		Owns:           []client.Object{&corev1.Secret{}, &v1.ObjectStorageConfig{}},
		Health:         a.Ping,
		HealthInterval: opts.HealthInterval,
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
