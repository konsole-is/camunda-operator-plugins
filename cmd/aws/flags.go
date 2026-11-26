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

package main

import (
	"flag"
	"time"

	"github.com/konsole-is/camunda-operator-plugins/internal/aws"
	"github.com/konsole-is/camunda-operator-plugins/internal/awsconnectorsrole"
	"github.com/konsole-is/camunda-operator-plugins/internal/awsebs"
	"github.com/konsole-is/camunda-operator-plugins/internal/awsidentity"
	"github.com/konsole-is/camunda-operator-plugins/internal/awskms"
	"github.com/konsole-is/camunda-operator-plugins/internal/awss3"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// flags are the values of the command line. A controller is on when its
// registrations flag names a file.
type flags struct {
	version bool
	manager aws.Options

	mode            string
	region          string
	extraTags       awskit.ExtraTags
	oidcProviderARN string
	eksClusterName  string

	identityRegistrations  string
	identityAllowedActions string
	identity               awsidentity.Options

	connectorsRoleRegistrations string
	connectorsRole              awsconnectorsrole.Options

	kmsRegistrations  string
	kmsAdministrators string
	kms               awskms.Options
	ebsRegistrations  string
	ebs               awsebs.Options
	s3Registrations   string
	s3                awss3.Options
}

// bindFlags binds the flags of the command to fs, with their defaults.
func bindFlags(fs *flag.FlagSet) *flags {
	f := &flags{}
	fs.BoolVar(&f.version, "version", false, "Print the version of the build and exit.")
	fs.StringVar(
		&f.manager.HealthProbeAddress,
		"health-probe-bind-address",
		":8081",
		"The address of /healthz and /readyz.",
	)
	fs.StringVar(
		&f.manager.MetricsAddress,
		"metrics-bind-address",
		"0",
		"The address of /metrics. 0 turns the metrics off.",
	)
	fs.BoolVar(
		&f.manager.LeaderElection,
		"leader-elect",
		false,
		"Run the controllers only in the replica that holds the leader election Lease.",
	)
	fs.StringVar(
		&f.manager.LeaderElectionID,
		"leader-election-id",
		"aws.plugins.camunda.io",
		"The name of the leader election Lease, in the namespace of the controller.",
	)

	fs.StringVar(&f.mode, "mode", string(awskit.ModeIRSA), "How the pods assume their role: irsa or podIdentity.")
	fs.StringVar(&f.region, "region", "", "The AWS region. Empty takes the region of the AWS SDK chain.")
	fs.Var(
		&f.extraTags,
		"extra-tags",
		"Tags, as comma-separated key=value items, that go on each AWS object next to the tags of the request.",
	)
	fs.StringVar(&f.oidcProviderARN, "oidc-provider-arn", "", "The IAM OIDC provider of the EKS cluster (irsa).")
	fs.StringVar(&f.eksClusterName, "eks-cluster-name", "", "The EKS cluster of the associations (podIdentity).")

	bindIdentity(fs, f)
	bindConnectorsRole(fs, f)
	bindKMS(fs, f)
	bindEBS(fs, f)
	bindS3(fs, f)

	return f
}

func bindIdentity(fs *flag.FlagSet, f *flags) {
	fs.StringVar(
		&f.identityRegistrations,
		"identity-registrations",
		"",
		"The file of the registrations of aws-identity. Empty turns aws-identity off.",
	)
	fs.StringVar(
		&f.identity.PermissionsBoundaryARN,
		"identity-permissions-boundary-arn",
		"",
		"The managed policy that bounds every role of aws-identity.",
	)
	fs.StringVar(
		&f.identity.RoleNamePrefix,
		"identity-role-name-prefix",
		"camunda",
		"The start of the name of each role of aws-identity. It must differ from the prefix of aws-connectors-role.",
	)
	fs.StringVar(
		&f.identityAllowedActions,
		"identity-allowed-actions",
		"",
		"The IAM action patterns, comma-separated, that a grant can put on a role.",
	)
	fs.DurationVar(
		&f.identity.Resync,
		"identity-resync",
		time.Hour,
		"The time after which aws-identity reconciles each request again. 0 turns it off.",
	)
}

func bindConnectorsRole(fs *flag.FlagSet, f *flags) {
	fs.StringVar(
		&f.connectorsRoleRegistrations,
		"connectors-role-registrations",
		"",
		"The file of the registrations of aws-connectors-role. Empty turns aws-connectors-role off.",
	)
	fs.StringVar(
		&f.connectorsRole.PermissionsBoundaryARN,
		"connectors-role-permissions-boundary-arn",
		"",
		"The managed policy that bounds each role of aws-connectors-role.",
	)
	fs.StringVar(
		&f.connectorsRole.RoleNamePrefix,
		"connectors-role-role-name-prefix",
		"camunda-connectors",
		"The start of the name of each role of aws-connectors-role. It must differ from the prefix of aws-identity.",
	)
	fs.DurationVar(
		&f.connectorsRole.Resync,
		"connectors-role-resync",
		time.Hour,
		"The time after which aws-connectors-role reconciles each request again. 0 turns it off.",
	)
}

func bindKMS(fs *flag.FlagSet, f *flags) {
	fs.StringVar(
		&f.kmsRegistrations,
		"kms-registrations",
		"",
		"The file of the registrations of aws-kms. Empty turns aws-kms off.",
	)
	fs.StringVar(&f.kms.Settings.AliasPrefix, "kms-alias-prefix", "camunda", "The prefix of each alias, after alias/.")
	fs.StringVar(
		&f.kmsAdministrators,
		"kms-key-administrator-arns",
		"",
		"The IAM principals, comma-separated, that administer each key.",
	)
	fs.StringVar(
		&f.kms.Settings.CSIDriverRoleARN,
		"kms-csi-driver-role-arn",
		"",
		"The IAM role of the EBS CSI driver, which may use each key.",
	)
	fs.DurationVar(
		&f.kms.Resync,
		"kms-resync",
		10*time.Minute,
		"The time after which aws-kms reconciles each request again. 0 turns it off.",
	)
}

func bindEBS(fs *flag.FlagSet, f *flags) {
	fs.StringVar(
		&f.ebsRegistrations,
		"ebs-registrations",
		"",
		"The file of the registrations of aws-ebs. Empty turns aws-ebs off.",
	)
	fs.DurationVar(
		&f.ebs.Resync,
		"ebs-resync",
		time.Hour,
		"The time after which aws-ebs reconciles each request again. 0 turns it off.",
	)
}

func bindS3(fs *flag.FlagSet, f *flags) {
	fs.StringVar(
		&f.s3Registrations,
		"s3-registrations",
		"",
		"The file of the registrations of aws-s3. Empty turns aws-s3 off.",
	)
	fs.StringVar(
		&f.s3.Settings.Partition,
		"s3-partition",
		"aws",
		"The AWS partition of the buckets, for their ARNs and their console links.",
	)
	fs.DurationVar(
		&f.s3.Settings.DeleteBudget,
		"s3-delete-budget",
		15*time.Second,
		"The time for which one pass of a delete deletes objects. A larger bucket takes more passes.",
	)
	fs.DurationVar(
		&f.s3.Resync,
		"s3-resync",
		time.Hour,
		"The time after which aws-s3 reconciles each request again. 0 turns it off.",
	)
}
