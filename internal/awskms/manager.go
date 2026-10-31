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

package awskms

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// name is the default controller name of the plugin.
const name = "aws-kms"

// Options are the settings of the manager that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-kms. Controller names are
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
	// Settings are the settings of the keys.
	Settings Settings
	// KMS returns the KMS client of a region.
	KMS func(region string) awskit.KMS
}

// NewManager returns a manager for cfg that publishes and serves the
// registrations of opts. It refuses settings that fail Settings.Validate and
// a nil KMS. /healthz and /readyz answer while it runs.
func NewManager(cfg *rest.Config, opts Options) (ctrl.Manager, error) {
	plugin, err := newPlugin(opts)
	if err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	for _, add := range []struct {
		what    string
		install func(*runtime.Scheme) error
	}{
		{"the Kubernetes kinds", clientgoscheme.AddToScheme},
		{"the plugins.camunda.io kinds", pluginsv1alpha1.AddToScheme},
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

	controller := opts.Name
	if controller == "" {
		controller = name
	}
	if err := pluginkit.Setup(mgr, pluginkit.Config{
		Name:           controller,
		Version:        version.Version(),
		Registrations:  opts.Registrations,
		ServiceAccount: opts.ServiceAccount,
		Health:         plugin.health,
		Resync:         resync,
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

// newPlugin returns the plugin of opts.
func newPlugin(opts Options) (*Plugin, error) {
	if err := opts.Settings.Validate(); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}

	if opts.KMS == nil {
		return nil, errors.New("the KMS client is missing")
	}

	return &Plugin{keys: keys{settings: opts.Settings, kms: opts.KMS}}, nil
}
