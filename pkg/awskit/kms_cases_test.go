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

package awskit_test

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

var kmsCases = []awsCase{
	{"finds a key by its alias in its region only", func(t *testing.T, a account) {
		ctx := context.Background()
		key := a.key(t, false)
		alias, err := a.alias(t, "kms-alias", aws.ToString(key.KeyId))
		require.NoError(t, err)

		assert.Regexp(
			t,
			"^"+regexp.QuoteMeta("arn:aws:kms:"+a.region+":"+a.id+":key/")+"[0-9a-f-]{36}$",
			aws.ToString(key.Arn),
		)
		a.eventually(t, func(c *assert.CollectT) {
			out, err := a.kmsIn(a.region).DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(alias)})
			require.NoError(c, err)
			assert.Equal(c, aws.ToString(key.Arn), aws.ToString(out.KeyMetadata.Arn))
			assert.Equal(c, kmstypes.KeyStateEnabled, out.KeyMetadata.KeyState)
		})
		_, err = a.kmsIn(a.replica).DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(alias)})
		assert.True(t, awskit.IsNotFound(err), "an alias is in one region: %v", err)
	}},
	{"refuses an alias that exists", func(t *testing.T, a account) {
		key := a.key(t, false)
		suffix := "kms-alias-twice"
		_, err := a.alias(t, suffix, aws.ToString(key.KeyId))
		require.NoError(t, err)

		_, err = a.alias(t, suffix, aws.ToString(key.KeyId))

		assert.True(t, awskit.IsAlreadyExists(err), "a second CreateAlias: %v", err)
	}},
	{"gives a key without a policy the default key policy", func(t *testing.T, a account) {
		key := a.key(t, false)

		out, err := a.kmsIn(a.region).GetKeyPolicy(context.Background(), &kms.GetKeyPolicyInput{KeyId: key.KeyId})

		require.NoError(t, err)
		type statement struct {
			Effect    string
			Principal struct{ AWS string }
			Action    string
		}
		// KMS formats the policy with white space, so the case reads it as JSON.
		var policy struct{ Statement []statement }
		require.NoError(t, json.Unmarshal([]byte(aws.ToString(out.Policy)), &policy))
		root := statement{Effect: "Allow", Action: "kms:*"}
		root.Principal.AWS = "arn:aws:iam::" + a.id + ":root"
		assert.Contains(t, policy.Statement, root)
	}},
	{"replicates a multi-Region key once", func(t *testing.T, a account) {
		ctx := context.Background()
		key := a.key(t, true)
		tags := a.tagged(map[string]string{"c": "d"})

		assert.Regexp(t, `^mrk-[0-9a-f]{32}$`, aws.ToString(key.KeyId))
		require.NoError(t, a.replicate(t, aws.ToString(key.KeyId), tags))
		replica := a.kmsIn(a.replica)
		a.eventually(t, func(c *assert.CollectT) {
			out, err := replica.ListResourceTags(ctx, &kms.ListResourceTagsInput{KeyId: key.KeyId})
			require.NoError(c, err)
			assert.ElementsMatch(c, kmsTags(tags), out.Tags)

			described, err := replica.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: key.KeyId})
			require.NoError(c, err)
			assert.Equal(c, kmstypes.KeyStateEnabled, described.KeyMetadata.KeyState)

			primary, err := a.kmsIn(a.region).DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: key.KeyId})
			require.NoError(c, err)
			config := primary.KeyMetadata.MultiRegionConfiguration
			require.NotNil(c, config)
			assert.Equal(c, kmstypes.MultiRegionKeyTypePrimary, config.MultiRegionKeyType)
			require.Len(c, config.ReplicaKeys, 1)
			assert.Equal(c, a.replica, aws.ToString(config.ReplicaKeys[0].Region))
		})
		err := a.replicate(t, aws.ToString(key.KeyId), tags)
		assert.True(t, awskit.IsAlreadyExists(err), "a second replica in one region: %v", err)
	}},
	{"gives a replica the rotation of its primary", func(t *testing.T, a account) {
		ctx := context.Background()
		key := a.key(t, true)
		_, err := a.kmsIn(a.region).EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{KeyId: key.KeyId})
		require.NoError(t, err)

		require.NoError(t, a.replicate(t, aws.ToString(key.KeyId), a.tags))

		a.eventually(t, func(c *assert.CollectT) {
			out, err := a.kmsIn(a.replica).GetKeyRotationStatus(
				ctx,
				&kms.GetKeyRotationStatusInput{KeyId: key.KeyId},
			)
			require.NoError(c, err)
			assert.True(c, out.KeyRotationEnabled)
		})
	}},
	{"refuses to replicate a single-Region key", func(t *testing.T, a account) {
		key := a.key(t, false)

		err := a.replicate(t, aws.ToString(key.KeyId), a.tags)

		assert.Equal(t, "UnsupportedOperationException", awskit.ErrorCode(err), "ReplicateKey: %v", err)
	}},
	{"schedules the deletion of a key in 7 days", func(t *testing.T, a account) {
		ctx := context.Background()
		key := a.key(t, false)
		c := a.kmsIn(a.region)
		schedule := func() error {
			_, err := c.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
				KeyId:               key.KeyId,
				PendingWindowInDays: aws.Int32(7),
			})
			return err
		}

		start := time.Now()
		require.NoError(t, schedule())
		end := time.Now()

		a.eventually(t, func(c *assert.CollectT) {
			out, err := a.kmsIn(a.region).DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: key.KeyId})
			require.NoError(c, err)
			assert.Equal(c, kmstypes.KeyStatePendingDeletion, out.KeyMetadata.KeyState)
			// KMS gives the window only for a primary key that waits for its
			// replicas.
			assert.Nil(c, out.KeyMetadata.PendingDeletionWindowInDays)
			require.NotNil(c, out.KeyMetadata.DeletionDate)
			// KMS can delete a key up to a day after the end of its window. The
			// bounds allow a minute of clock skew.
			date := *out.KeyMetadata.DeletionDate
			assert.False(c, date.Before(start.Add(7*24*time.Hour-time.Minute)), "the deletion date %s", date)
			assert.False(c, date.After(end.Add(8*24*time.Hour+time.Minute)), "the deletion date %s", date)
		})
		err := schedule()
		assert.Equal(t, "KMSInvalidStateException", awskit.ErrorCode(err), "a second schedule: %v", err)
	}},
	{"holds a primary with a replica in PendingReplicaDeletion", func(t *testing.T, a account) {
		ctx := context.Background()
		key := a.key(t, true)
		require.NoError(t, a.replicate(t, aws.ToString(key.KeyId), a.tags))
		a.eventually(t, func(c *assert.CollectT) {
			out, err := a.kmsIn(a.replica).DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: key.KeyId})
			require.NoError(c, err)
			assert.Equal(c, kmstypes.KeyStateEnabled, out.KeyMetadata.KeyState)
		})

		_, err := a.kmsIn(a.region).ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
			KeyId:               key.KeyId,
			PendingWindowInDays: aws.Int32(7),
		})

		require.NoError(t, err)
		a.eventually(t, func(c *assert.CollectT) {
			out, err := a.kmsIn(a.region).DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: key.KeyId})
			require.NoError(c, err)
			assert.Equal(c, kmstypes.KeyStatePendingReplicaDeletion, out.KeyMetadata.KeyState)
			assert.Equal(c, int32(7), aws.ToInt32(out.KeyMetadata.PendingDeletionWindowInDays))
			assert.Nil(c, out.KeyMetadata.DeletionDate, "the window starts after the replicas are deleted")
		})
	}},
}

// replicate replicates the multi-Region key id into the replica region of
// the account, with tags, and schedules the deletion of the replica at the
// end of the case. It returns the error of ReplicateKey.
func (a account) replicate(t *testing.T, id string, tags map[string]string) error {
	t.Cleanup(func() { a.removeKey(t, a.replica, id) })

	_, err := a.kmsIn(a.region).ReplicateKey(context.Background(), &kms.ReplicateKeyInput{
		KeyId:         aws.String(id),
		ReplicaRegion: aws.String(a.replica),
		Tags:          kmsTags(tags),
	})
	return err
}
