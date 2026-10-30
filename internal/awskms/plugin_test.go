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
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

const (
	keyARN     = "arn:aws:kms:eu-west-1:111122223333:key/mrk-1"
	replicaARN = "arn:aws:kms:eu-central-1:111122223333:key/mrk-1"
)

func enabledKey() keyStatus {
	return keyStatus{
		ARN:      keyARN,
		ID:       "mrk-1",
		Alias:    wantAlias,
		Region:   home,
		State:    types.KeyStateEnabled,
		Rotation: true,
	}
}

func request() *pluginsv1alpha1.PluginRequest {
	return &pluginsv1alpha1.PluginRequest{
		Spec: pluginsv1alpha1.PluginRequestSpec{
			Target: pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: "orders"},
		},
	}
}

func TestResultPublishesTheKeyARNAndFormat(t *testing.T) {
	res, err := result(enabledKey(), request())

	require.NoError(t, err)
	assert.Equal(t, map[string]string{"keyRef": keyARN, "format": "aws-kms-arn"}, res.Outputs)
	assert.Nil(t, res.NotReady)
	assert.Equal(t, []pluginsv1alpha1.SummaryItem{
		{Label: "Key state", Value: "Enabled"},
		{Label: "Rotation", Value: "On"},
		{Label: "Alias", Value: wantAlias},
	}, res.Summary)
	assert.Equal(t, []pluginsv1alpha1.Link{{
		Name: "KMS key",
		URL:  "https://eu-west-1.console.aws.amazon.com/kms/home?region=eu-west-1#/kms/keys/mrk-1",
	}}, res.Links)
	assert.Equal(t, []pluginsv1alpha1.ResourceRef{{
		Kind:     "KMSKey",
		Name:     wantAlias,
		ID:       keyARN,
		Location: home,
		Role:     "Encryption key of the cluster orders",
		OnOrphan: pluginsv1alpha1.OnOrphanKept,
	}}, res.Resources)
}

func TestResultShowsThePendingDeletionAsError(t *testing.T) {
	status := enabledKey()
	status.State = types.KeyStatePendingDeletion
	date := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	status.DeletionDate = &date

	res, err := result(status, request())

	require.NoError(t, err)
	require.NotNil(t, res.NotReady)
	assert.Equal(t, ReasonKeyPendingDeletion, res.NotReady.Reason)
	assert.Equal(t, keyARN, res.Outputs["keyRef"])
	assert.Contains(t, res.Summary, pluginsv1alpha1.SummaryItem{
		Label:    "Deletion",
		Value:    "Pending. AWS deletes the key on 2026-11-06.",
		Severity: pluginsv1alpha1.SeverityError,
	})
	assert.Equal(t, pluginsv1alpha1.SummaryItem{Label: "Key state", Value: "PendingDeletion"}, res.Summary[0])
}

func TestResultReportsAKeyThatIsNotEnabled(t *testing.T) {
	status := enabledKey()
	status.State = types.KeyStateDisabled

	res, err := result(status, request())

	require.NoError(t, err)
	require.NotNil(t, res.NotReady)
	assert.Equal(t, ReasonKeyNotEnabled, res.NotReady.Reason)
	assert.Contains(t, res.NotReady.Message, "Disabled")
}

func TestResultWaitsForReplicas(t *testing.T) {
	status := enabledKey()
	status.MultiRegion = true
	status.Replicas = []replicaStatus{{Region: replica, ARN: replicaARN, State: types.KeyStateCreating}}

	res, err := result(status, request())

	require.NoError(t, err)
	require.NotNil(t, res.NotReady)
	assert.Equal(t, ReasonReplicasPending, res.NotReady.Reason)
	assert.Equal(t, 5*time.Second, res.RequeueAfter)
	assert.Contains(t, res.Summary, pluginsv1alpha1.SummaryItem{Label: "Replicas", Value: "eu-central-1 (Creating)"})
	require.Len(t, res.Resources, 2)
	assert.Equal(t, pluginsv1alpha1.ResourceRef{
		Kind:     "KMSKey",
		Name:     "mrk-1",
		ID:       replicaARN,
		Location: replica,
		Role:     "Replica of the key",
		OnOrphan: pluginsv1alpha1.OnOrphanKept,
	}, res.Resources[1])
}

func TestResultWarnsOfAKeptReplica(t *testing.T) {
	status := enabledKey()
	status.ExtraReplicas = []replicaStatus{{Region: "us-east-1", ARN: "arn:aws:kms:us-east-1:111122223333:key/mrk-1"}}

	res, err := result(status, request())

	require.NoError(t, err)
	assert.Nil(t, res.NotReady)
	assert.Contains(t, res.Summary, pluginsv1alpha1.SummaryItem{
		Label:    "Replica kept",
		Value:    "us-east-1 is not in replicaRegions. The replica stays until the request is deleted.",
		Severity: pluginsv1alpha1.SeverityWarning,
	})
	assert.Len(t, res.Resources, 2)
}

func TestNewPluginRefusesBadSettings(t *testing.T) {
	_, err := newPlugin(Options{KMS: awsfake.NewKMS().In})
	assert.ErrorContains(t, err, "region")

	_, err = newPlugin(Options{Settings: validSettings()})
	assert.ErrorContains(t, err, "KMS client")

	_, err = newPlugin(Options{Settings: validSettings(), KMS: func(string) awskit.KMS { return nil }})
	assert.NoError(t, err)
}
