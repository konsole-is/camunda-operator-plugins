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

package awsfake

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// createKey creates a key with the tag a=b in the home region of f.
func createKey(t *testing.T, f *KMS, multiRegion bool) *types.KeyMetadata {
	out, err := f.CreateKey(context.Background(), &kms.CreateKeyInput{
		MultiRegion: aws.Bool(multiRegion),
		Tags:        []types.Tag{{TagKey: aws.String("a"), TagValue: aws.String("b")}},
	})
	require.NoError(t, err)
	return out.KeyMetadata
}

// alias creates the alias alias/x of the key keyID.
func alias(f awskit.KMS, keyID string) error {
	_, err := f.CreateAlias(context.Background(), &kms.CreateAliasInput{
		AliasName:   aws.String("alias/x"),
		TargetKeyId: aws.String(keyID),
	})
	return err
}

func TestKMSKeepsTheDefaultPolicyWithoutAPolicy(t *testing.T) {
	f := NewKMS()

	key := createKey(t, f, false)

	got, _ := f.Key("eu-west-1", aws.ToString(key.KeyId))
	assert.True(t, got.DefaultPolicy)
}

func TestKMSReplicatesAMultiRegionKey(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, true)
	describe := func() *types.KeyMetadata {
		out, err := f.In("eu-central-1").DescribeKey(context.Background(), &kms.DescribeKeyInput{KeyId: key.KeyId})
		require.NoError(t, err)
		return out.KeyMetadata
	}

	_, err := f.ReplicateKey(context.Background(), &kms.ReplicateKeyInput{
		KeyId:         key.KeyId,
		ReplicaRegion: aws.String("eu-central-1"),
	})

	require.NoError(t, err)
	replica, ok := f.Key("eu-central-1", aws.ToString(key.KeyId))
	require.True(t, ok)
	assert.Equal(t, "eu-west-1", replica.Primary)
	assert.Equal(t, types.KeyStateCreating, describe().KeyState)
	assert.Equal(t, types.KeyStateEnabled, describe().KeyState)
}

func TestKMSRefusesAWindowOutsideSevenToThirtyDays(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, false)
	schedule := func(days int32) error {
		_, err := f.ScheduleKeyDeletion(context.Background(), &kms.ScheduleKeyDeletionInput{
			KeyId:               key.KeyId,
			PendingWindowInDays: aws.Int32(days),
		})
		return err
	}

	assert.Equal(t, "ValidationException", awskit.ErrorCode(schedule(6)))
	assert.Equal(t, "ValidationException", awskit.ErrorCode(schedule(31)))
	got, _ := f.Key("eu-west-1", aws.ToString(key.KeyId))
	assert.Equal(t, types.KeyStateEnabled, got.State)
}

func TestKMSHoldsAPrimaryUntilItsReplicasArePending(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, true)
	_, err := f.ReplicateKey(context.Background(), &kms.ReplicateKeyInput{
		KeyId:         key.KeyId,
		ReplicaRegion: aws.String("eu-central-1"),
	})
	require.NoError(t, err)

	_, err = f.ScheduleKeyDeletion(context.Background(), &kms.ScheduleKeyDeletionInput{KeyId: key.KeyId})

	require.NoError(t, err)
	got, _ := f.Key("eu-west-1", aws.ToString(key.KeyId))
	assert.Equal(t, types.KeyStatePendingReplicaDeletion, got.State)
	assert.Equal(t, int32(30), got.PendingWindowDays)
}

func TestKMSRefusesToScheduleAKeyThatIsCreating(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, false)
	f.SetState("eu-west-1", aws.ToString(key.KeyId), types.KeyStateCreating)

	_, err := f.ScheduleKeyDeletion(context.Background(), &kms.ScheduleKeyDeletionInput{KeyId: key.KeyId})

	assert.Equal(t, "KMSInvalidStateException", awskit.ErrorCode(err))
}

func TestKMSFailNextFailsOneCall(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, false)
	f.FailNext("CreateAlias", errors.New("boom"))

	assert.EqualError(t, alias(f.In("eu-west-1"), aws.ToString(key.KeyId)), "boom")
	assert.NoError(t, alias(f, aws.ToString(key.KeyId)))
	assert.Equal(t, []string{"CreateKey", "CreateAlias", "CreateAlias"}, f.Calls())
}
