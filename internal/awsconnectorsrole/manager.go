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

// Package awsconnectorsrole gives the Connectors of each Camunda cluster an
// AWS IAM role of their own. It serves the connectors-permissions section
// through pluginkit, with a role that the plugin owns for each request, or
// with one role that the admin made.
package awsconnectorsrole

import (
	"errors"
	"fmt"
	"strings"

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

// Options are the settings of the manager that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-connectors-role. Controller
	// names are unique in a process, so two managers in one process need two
	// names.
	Name string
	// HealthProbeAddress is the address of /healthz and /readyz, for example
	// ":8081". "0" turns the probes off.
	HealthProbeAddress string
	// MetricsAddress is the address of /metrics. "0" turns the metrics off.
	MetricsAddress string
	// Registrations are the registrations that the plugin publishes and
	// serves: RegistrationRole, RegistrationExistingRole, or both.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// LeaderElection makes only the replica that holds the Lease
	// LeaderElectionID write. It needs a LeaderElectionID.
	LeaderElection   bool
	LeaderElectionID string
	// Mode is how the Connectors pods assume their role.
	Mode awskit.Mode
	// RoleNamePrefix starts the name of each role of RegistrationRole. It
	// must differ from the prefix of aws-identity.
	RoleNamePrefix string
	// PermissionsBoundaryARN is the managed policy that bounds each role of
	// RegistrationRole.
	PermissionsBoundaryARN string
	// ExtraTags go on each AWS object, next to the tags of the request.
	ExtraTags awskit.ExtraTags
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
// registrations of opts. It refuses a registration that the plugin does not
// know, an unknown mode, ModeIRSA without a valid OIDC provider ARN,
// ModePodIdentity without an EKS cluster name or an EKS client, and a
// missing IAM or STS client. When opts serves RegistrationRole, it also
// refuses no permissions boundary, and a role name prefix that IAM refuses
// or that is the prefix of aws-identity. /healthz and /readyz answer while
// it runs.
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
	servesRole := false
	for _, r := range opts.Registrations {
		switch r.Name {
		case RegistrationRole:
			servesRole = true
		case RegistrationExistingRole:
		default:
			return nil, fmt.Errorf(
				"the registration %q is not %s or %s",
				r.Name,
				RegistrationRole,
				RegistrationExistingRole,
			)
		}
	}

	mode, err := awskit.ParseMode(string(opts.Mode))
	if err != nil {
		return nil, fmt.Errorf("mode: %w", err)
	}

	p := &Plugin{mode: mode, extraTags: opts.ExtraTags, clients: opts.Clients}
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

	if !servesRole {
		return p, nil
	}

	if opts.PermissionsBoundaryARN == "" {
		return nil, fmt.Errorf(
			"the permissions boundary ARN is empty: every role of %s needs a boundary",
			RegistrationRole,
		)
	}
	if err := awskit.CheckRolePrefix(opts.RoleNamePrefix); err != nil {
		return nil, err
	}
	// IAM role names are unique without regard to case.
	if strings.EqualFold(opts.RoleNamePrefix, awsIdentityRolePrefix) {
		return nil, fmt.Errorf(
			"role name prefix %q: aws-identity names its roles with it, so both plugins would write one role",
			opts.RoleNamePrefix,
		)
	}
	p.boundary = opts.PermissionsBoundaryARN
	p.prefix = opts.RoleNamePrefix

	return p, nil
}
