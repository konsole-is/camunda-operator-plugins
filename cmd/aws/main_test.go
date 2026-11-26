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
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
)

var account = pluginsv1alpha1.ServiceAccountRef{Namespace: "camunda-operator-plugins", Name: "aws"}

// parse returns the flags of args.
func parse(t *testing.T, args ...string) *flags {
	t.Helper()
	fs := flag.NewFlagSet("aws", flag.ContinueOnError)
	f := bindFlags(fs)
	require.NoError(t, fs.Parse(args))

	return f
}

// registrationsFile writes a registrations file with one registration name.
func registrationsFile(t *testing.T, name string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), name+".yaml")
	require.NoError(t, os.WriteFile(file, []byte("- name: "+name+"\n"), 0o600))

	return file
}

// noAWS is a loadConfig that fails the test when it is called.
func noAWS(t *testing.T) loadConfig {
	return func(context.Context, string) (awssdk.Config, error) {
		t.Error("the AWS config was loaded")
		return awssdk.Config{}, errors.New("no AWS in this test")
	}
}

func TestManagerOptionsTurnsOnOnlyTheControllersWithRegistrations(t *testing.T) {
	f := parse(t, "-ebs-registrations="+registrationsFile(t, "aws-ebs-encrypted"))

	opts, err := managerOptions(context.Background(), f, account, noAWS(t))

	require.NoError(t, err)
	require.NotNil(t, opts.EBS)
	assert.Equal(t, "aws-ebs-encrypted", opts.EBS.Registrations[0].Name)
	assert.Equal(t, account, opts.EBS.ServiceAccount)
	assert.Equal(t, time.Hour, opts.EBS.Resync)
	assert.Nil(t, opts.Identity)
	assert.Nil(t, opts.ConnectorsRole)
	assert.Nil(t, opts.KMS)
	assert.Nil(t, opts.S3)
	assert.Equal(t, []string{"aws-ebs"}, controllerNames(opts))
}

func TestManagerOptionsLoadsNoAWSConfigForAWSEBSAlone(t *testing.T) {
	f := parse(t, "-ebs-registrations="+registrationsFile(t, "aws-ebs-encrypted"), "-region=")

	_, err := managerOptions(context.Background(), f, account, noAWS(t))

	assert.NoError(t, err)
}

func TestManagerOptionsTurnsOnNoControllerWithoutRegistrations(t *testing.T) {
	opts, err := managerOptions(context.Background(), parse(t), account, noAWS(t))

	require.NoError(t, err)
	assert.Empty(t, controllerNames(opts))
}

func TestManagerOptionsKeepsTheManagerFlags(t *testing.T) {
	f := parse(t, "-leader-elect", "-leader-election-id=team-aws", "-health-probe-bind-address=:9000")

	opts, err := managerOptions(context.Background(), f, account, noAWS(t))

	require.NoError(t, err)
	assert.True(t, opts.LeaderElection)
	assert.Equal(t, "team-aws", opts.LeaderElectionID)
	assert.Equal(t, ":9000", opts.HealthProbeAddress)
	assert.Equal(t, "0", opts.MetricsAddress)
}

func TestManagerOptionsGivesEachControllerTheSharedValuesAndItsOwn(t *testing.T) {
	f := parse(t,
		"-mode=podIdentity",
		"-region=eu-west-1",
		"-extra-tags=team=a",
		"-eks-cluster-name=prod",
		"-identity-registrations="+registrationsFile(t, "aws-pod-identity"),
		"-identity-role-name-prefix=hub",
		"-identity-allowed-actions=s3:*, kms:Decrypt,",
		"-connectors-role-registrations="+registrationsFile(t, "aws-connectors-role"),
		"-connectors-role-role-name-prefix=conn",
	)
	load := func(_ context.Context, region string) (awssdk.Config, error) {
		return awssdk.Config{Region: region}, nil
	}

	opts, err := managerOptions(context.Background(), f, account, load)

	require.NoError(t, err)
	require.NotNil(t, opts.Identity)
	require.NotNil(t, opts.ConnectorsRole)
	assert.Equal(t, "hub", opts.Identity.RoleNamePrefix)
	assert.Equal(t, []string{"s3:*", "kms:Decrypt"}, opts.Identity.AllowedActions)
	assert.Equal(t, "conn", opts.ConnectorsRole.RoleNamePrefix)
	assert.Equal(t, "eu-west-1", opts.ConnectorsRole.Region)
	for _, got := range []struct {
		mode    string
		tags    map[string]string
		cluster string
	}{
		{string(opts.Identity.Mode), opts.Identity.ExtraTags, opts.Identity.EKSClusterName},
		{string(opts.ConnectorsRole.Mode), opts.ConnectorsRole.ExtraTags, opts.ConnectorsRole.EKSClusterName},
	} {
		assert.Equal(t, "podIdentity", got.mode)
		assert.Equal(t, map[string]string{"team": "a"}, got.tags)
		assert.Equal(t, "prod", got.cluster)
	}
	assert.Equal(t, []string{"aws-identity", "aws-connectors-role"}, controllerNames(opts))
}

func TestManagerOptionsReturnsTheErrorOfTheAWSConfig(t *testing.T) {
	f := parse(t, "-identity-registrations="+registrationsFile(t, "aws-irsa"))
	load := func(context.Context, string) (awssdk.Config, error) {
		return awssdk.Config{}, errors.New("no credentials")
	}

	_, err := managerOptions(context.Background(), f, account, load)

	assert.ErrorContains(t, err, "no credentials")
}

func TestManagerOptionsRefusesKMSAndS3WithoutARegion(t *testing.T) {
	for flagName, registration := range map[string]string{
		"-kms-registrations": "aws-kms-key",
		"-s3-registrations":  "aws-s3",
	} {
		t.Run(registration, func(t *testing.T) {
			f := parse(t, flagName+"="+registrationsFile(t, registration))
			load := func(context.Context, string) (awssdk.Config, error) { return awssdk.Config{}, nil }

			_, err := managerOptions(context.Background(), f, account, load)

			assert.ErrorContains(t, err, "could not find the AWS region: set -region or AWS_REGION")
		})
	}
}

func TestManagerOptionsNamesTheControllerOfARegistrationsFileThatDoesNotLoad(t *testing.T) {
	f := parse(t, "-kms-registrations="+filepath.Join(t.TempDir(), "missing.yaml"))

	_, err := managerOptions(context.Background(), f, account, noAWS(t))

	assert.ErrorContains(t, err, "loading the registrations of aws-kms")
}

func TestManagerOptionsRefusesAnUnknownMode(t *testing.T) {
	_, err := managerOptions(context.Background(), parse(t, "-mode=eks"), account, noAWS(t))

	assert.ErrorContains(t, err, "reading -mode")
}
