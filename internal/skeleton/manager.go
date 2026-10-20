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

// Package skeleton is the plugin that a new plugin copies. It serves its
// registrations through pluginkit and writes one ConfigMap for each request.
package skeleton

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// name is the controller name of the skeleton.
const name = "skeleton"

// Options are the settings of the manager that the command line sets.
type Options struct {
	// HealthProbeAddress is the address of /healthz and /readyz, for example
	// ":8081". "0" turns the probes off.
	HealthProbeAddress string
	// MetricsAddress is the address of /metrics. "0" turns the metrics off.
	MetricsAddress string
	// Registrations are the registrations that the skeleton publishes and
	// serves. NewManager refuses an empty list.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// LeaderElection makes only the replica that holds the Lease
	// LeaderElectionID write. It needs a LeaderElectionID.
	LeaderElection   bool
	LeaderElectionID string
}

// NewManager returns a manager for cfg that publishes and serves the
// registrations of opts. Its scheme holds the Kubernetes kinds and the
// plugins.camunda.io kinds of the api module, and /healthz and /readyz answer
// while it runs.
func NewManager(cfg *rest.Config, opts Options) (ctrl.Manager, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding the Kubernetes kinds to the scheme: %w", err)
	}

	if err := pluginsv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding the plugins.camunda.io kinds to the scheme: %w", err)
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

	if err := pluginkit.Setup(mgr, pluginkit.Config{
		Name:           name,
		Version:        version.Version(),
		Registrations:  opts.Registrations,
		ServiceAccount: opts.ServiceAccount,
		Owns:           []client.Object{&corev1.ConfigMap{}},
	}, Plugin{}); err != nil {
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
