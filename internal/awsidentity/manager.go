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

// Package awsidentity is the AWS identity hub. It serves the cloud-identity
// section through pluginkit: it gives each Camunda cluster one IAM role that
// its pods assume with IRSA or EKS Pod Identity, and it puts the Grants of
// other plugins for that cluster on the role as inline policies.
package awsidentity

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
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// Options are the settings of the manager that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-identity. Controller names
	// are unique in a process, so two managers in one process need two names.
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
	// Mode is how the pods assume the roles.
	Mode awskit.Mode
	// PermissionsBoundaryARN is the managed policy that bounds every role.
	PermissionsBoundaryARN string
	// AllowedActions are the IAM action patterns that a Grant may hold.
	AllowedActions []string
	// OIDCProviderARN is the IAM OIDC provider of the EKS cluster. ModeIRSA
	// needs it.
	OIDCProviderARN string
	// EKSClusterName is the EKS cluster of the associations. ModePodIdentity
	// needs it.
	EKSClusterName string
	// Clients are the AWS clients. NewManager needs IAM and STS, and EKS with
	// ModePodIdentity.
	Clients awskit.Clients
}

// NewManager returns a manager for cfg that publishes and serves the
// registrations of opts. It refuses an unknown mode, no permissions
// boundary, no allowed action or a bad one, ModeIRSA without a valid OIDC
// provider ARN, ModePodIdentity without an EKS cluster name, and a missing
// client. /healthz and /readyz answer while it runs.
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

	plugin.client = mgr.GetClient()
	plugin.reader = mgr.GetAPIReader()

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
		Watches:        watches(mgr.GetClient()),
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

// newPlugin returns the plugin of opts, without its Kubernetes clients.
func newPlugin(opts Options) (*Plugin, error) {
	mode, err := awskit.ParseMode(string(opts.Mode))
	if err != nil {
		return nil, fmt.Errorf("mode: %w", err)
	}

	if opts.PermissionsBoundaryARN == "" {
		return nil, errors.New("the permissions boundary ARN is empty: every role needs a boundary")
	}

	ceiling, err := NewCeiling(opts.AllowedActions)
	if err != nil {
		return nil, fmt.Errorf("allowed actions: %w", err)
	}

	p := &Plugin{
		mode:     mode,
		boundary: opts.PermissionsBoundaryARN,
		ceiling:  ceiling,
		clients:  opts.Clients,
	}

	switch mode {
	case awskit.ModeIRSA:
		if p.provider, err = awskit.ParseOIDCProvider(opts.OIDCProviderARN); err != nil {
			return nil, fmt.Errorf("the OIDC provider ARN of mode %s: %w", mode, err)
		}
	case awskit.ModePodIdentity:
		if opts.EKSClusterName == "" {
			return nil, fmt.Errorf("the EKS cluster name is empty: mode %s needs it", mode)
		}
		if opts.Clients.EKS == nil {
			return nil, fmt.Errorf("the EKS client is missing: mode %s needs it", mode)
		}
		p.eksCluster = opts.EKSClusterName
	}

	if opts.Clients.IAM == nil || opts.Clients.STS == nil {
		return nil, errors.New("the IAM or the STS client is missing")
	}

	return p, nil
}
