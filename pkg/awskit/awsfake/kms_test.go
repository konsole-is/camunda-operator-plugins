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
	"time"

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

func TestKMSFindsAKeyByItsAlias(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, false)
	require.NoError(t, alias(f, aws.ToString(key.KeyId)))

	out, err := f.DescribeKey(context.Background(), &kms.DescribeKeyInput{KeyId: aws.String("alias/x")})

	require.NoError(t, err)
	assert.Equal(t, aws.ToString(key.Arn), aws.ToString(out.KeyMetadata.Arn))
	assert.Equal(t, types.KeyStateEnabled, out.KeyMetadata.KeyState)
	assert.Regexp(t, `^arn:aws:kms:eu-west-1:111122223333:key/[0-9a-f-]{36}$`, aws.ToString(key.Arn))

	_, err = f.In("eu-central-1").DescribeKey(context.Background(), &kms.DescribeKeyInput{KeyId: aws.String("alias/x")})
	assert.True(t, awskit.IsNotFound(err), "an alias is in one region")
}

func TestKMSRefusesASecondAlias(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, false)
	require.NoError(t, alias(f, aws.ToString(key.KeyId)))

	err := alias(f, aws.ToString(key.KeyId))

	assert.True(t, awskit.IsAlreadyExists(err))
}

func TestKMSKeepsTheDefaultPolicyWithoutAPolicy(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, false)

	out, err := f.GetKeyPolicy(context.Background(), &kms.GetKeyPolicyInput{KeyId: key.KeyId})

	require.NoError(t, err)
	got, _ := f.Key("eu-west-1", aws.ToString(key.KeyId))
	assert.True(t, got.DefaultPolicy)
	assert.Contains(
		t,
		aws.ToString(out.Policy),
		`"Principal":{"AWS":"arn:aws:iam::111122223333:root"},"Action":"kms:*"`,
	)
}

func TestKMSReplicatesAMultiRegionKey(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, true)
	replicate := func() error {
		_, err := f.ReplicateKey(context.Background(), &kms.ReplicateKeyInput{
			KeyId:         key.KeyId,
			ReplicaRegion: aws.String("eu-central-1"),
			Tags:          []types.Tag{{TagKey: aws.String("c"), TagValue: aws.String("d")}},
		})
		return err
	}
	describe := func() *types.KeyMetadata {
		out, err := f.In("eu-central-1").DescribeKey(context.Background(), &kms.DescribeKeyInput{KeyId: key.KeyId})
		require.NoError(t, err)
		return out.KeyMetadata
	}

	require.NoError(t, replicate())

	assert.Regexp(t, `^mrk-[0-9a-f]{32}$`, aws.ToString(key.KeyId))
	replica, ok := f.Key("eu-central-1", aws.ToString(key.KeyId))
	require.True(t, ok)
	assert.Equal(t, map[string]string{"c": "d"}, replica.Tags)
	assert.Equal(t, "eu-west-1", replica.Primary)
	assert.Equal(t, types.KeyStateCreating, describe().KeyState)
	assert.Equal(t, types.KeyStateEnabled, describe().KeyState)

	primary, err := f.DescribeKey(context.Background(), &kms.DescribeKeyInput{KeyId: key.KeyId})
	require.NoError(t, err)
	config := primary.KeyMetadata.MultiRegionConfiguration
	assert.Equal(t, types.MultiRegionKeyTypePrimary, config.MultiRegionKeyType)
	require.Len(t, config.ReplicaKeys, 1)
	assert.Equal(t, "eu-central-1", aws.ToString(config.ReplicaKeys[0].Region))
	assert.True(t, awskit.IsAlreadyExists(replicate()))
}

func TestKMSRefusesToReplicateASingleRegionKey(t *testing.T) {
	f := NewKMS()
	key := createKey(t, f, false)

	_, err := f.ReplicateKey(context.Background(), &kms.ReplicateKeyInput{
		KeyId:         key.KeyId,
		ReplicaRegion: aws.String("eu-central-1"),
	})

	assert.Equal(t, "UnsupportedOperationException", awskit.ErrorCode(err))
}

func TestKMSSchedulesDeletionInsideTheWindow(t *testing.T) {
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
	require.NoError(t, schedule(7))

	got, _ := f.Key("eu-west-1", aws.ToString(key.KeyId))
	assert.Equal(t, types.KeyStatePendingDeletion, got.State)
	assert.Equal(t, int32(7), got.PendingWindowDays)
	assert.WithinDuration(t, time.Now().Add(7*24*time.Hour), *got.DeletionDate, time.Minute)
	assert.Equal(t, "KMSInvalidStateException", awskit.ErrorCode(schedule(7)))
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
	assert.Nil(t, got.DeletionDate, "the window starts after the replicas are deleted")
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
