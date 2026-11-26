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
	"cmp"
	"errors"
	"fmt"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/internal/version"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// Options are the settings of the controller that the command line sets.
type Options struct {
	// Name is the controller name. Empty is aws-identity. Controller names
	// are unique in a process, so two controllers in one process need two
	// names.
	Name string
	// Registrations are the registrations that the plugin publishes and
	// serves. Setup refuses an empty list.
	Registrations []pluginsv1alpha1.PluginRegistration
	// ServiceAccount is the ServiceAccount of the controller.
	ServiceAccount pluginsv1alpha1.ServiceAccountRef
	// Mode is how the pods assume the roles.
	Mode awskit.Mode
	// PermissionsBoundaryARN is the managed policy that bounds every role.
	PermissionsBoundaryARN string
	// RoleNamePrefix starts the name of each role.
	RoleNamePrefix string
	// ExtraTags go on each role and each association, next to the request
	// tags.
	ExtraTags awskit.ExtraTags
	// AllowedActions are the IAM action patterns that a Grant may hold.
	AllowedActions []string
	// OIDCProviderARN is the IAM OIDC provider of the EKS cluster. ModeIRSA
	// needs it.
	OIDCProviderARN string
	// EKSClusterName is the EKS cluster of the associations. ModePodIdentity
	// needs it.
	EKSClusterName string
	// Clients are the AWS clients. Setup needs IAM and STS, and EKS with
	// ModePodIdentity.
	Clients awskit.Clients
	// Resync is the pluginkit.Config.Resync of the plugin.
	Resync time.Duration
}

// Setup adds the controller of opts to mgr: its registration publisher and
// the controller that serves its requests. The scheme of mgr must hold the
// Kubernetes kinds, the plugins.camunda.io kinds, and the core.camunda.io
// kinds. It refuses an unknown mode, no permissions boundary, a role name
// prefix that awskit.CheckRolePrefix refuses or that aws-connectors-role uses
// by default, more extra tags than awskit.CheckExtraTags allows next to the
// UID tag and the desired tag, no allowed action or a bad one, ModeIRSA
// without a valid OIDC provider ARN, ModePodIdentity without an EKS cluster
// name, and a missing client.
func Setup(mgr ctrl.Manager, opts Options) error {
	plugin, err := newPlugin(opts)
	if err != nil {
		return err
	}

	plugin.client = mgr.GetClient()
	plugin.reader = mgr.GetAPIReader()

	return pluginkit.Setup(mgr, pluginkit.Config{
		Name:           cmp.Or(opts.Name, name),
		Version:        version.Version(),
		Registrations:  opts.Registrations,
		ServiceAccount: opts.ServiceAccount,
		Health:         plugin.health,
		Resync:         opts.Resync,
		Watches:        watches(mgr.GetClient()),
	}, plugin)
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

	if err := awskit.CheckRolePrefix(opts.RoleNamePrefix); err != nil {
		return nil, err
	}
	// IAM role names are unique without regard to case.
	if strings.EqualFold(opts.RoleNamePrefix, connectorsRolePrefix) {
		return nil, fmt.Errorf(
			"role name prefix %q: aws-connectors-role names its roles with it, so both plugins would write one role",
			opts.RoleNamePrefix,
		)
	}

	if err := awskit.CheckExtraTags(opts.ExtraTags, ownTags); err != nil {
		return nil, fmt.Errorf("extra tags: %w", err)
	}

	ceiling, err := NewCeiling(opts.AllowedActions)
	if err != nil {
		return nil, fmt.Errorf("allowed actions: %w", err)
	}

	p := &Plugin{
		mode:      mode,
		boundary:  opts.PermissionsBoundaryARN,
		prefix:    opts.RoleNamePrefix,
		extraTags: opts.ExtraTags,
		ceiling:   ceiling,
		clients:   opts.Clients,
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
