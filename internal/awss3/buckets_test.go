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

package awss3

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

const (
	keyARN   = "arn:aws:kms:eu-west-1:111122223333:key/0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0"
	otherUID = "uid-other"
)

// testRef is the bucket of the request orders-backup of the claim orders.
func testRef(region string) bucketRef {
	return bucketRef{
		Namespace: "team-a",
		Claim:     "orders",
		Request:   "orders-backup",
		UID:       "uid-1",
		Name:      "camunda-team-a-orders-0123456789abcdef",
		Region:    region,
	}
}

func testWant(ref bucketRef) want {
	return want{Tags: bucketTags(Settings{ExtraTags: awskit.ExtraTags{"team": "platform"}}, ref), AbortDays: 7}
}

// newBuckets returns the buckets of a new fake, with a delete budget of one
// minute.
func newBuckets() (buckets, *awsfake.S3) {
	f := awsfake.NewS3()
	return buckets{s3: f.In, deleteBudget: time.Minute}, f
}

// ensured returns buckets whose bucket of testRef exists, as the plugin
// created it.
func ensured(t *testing.T) (buckets, *awsfake.S3, bucketRef) {
	b, f := newBuckets()
	ref := testRef("eu-west-1")
	_, err := b.ensure(context.Background(), ref, testWant(ref), prior{})
	require.NoError(t, err)
	return b, f, ref
}

// createInput creates the bucket name in region without tags, as a person
// outside the plugin does.
func createInput(name, region string) *s3.CreateBucketInput {
	return &s3.CreateBucketInput{
		Bucket: aws.String(name),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		},
	}
}

func bucketOfFake(t *testing.T, f *awsfake.S3, name string) awsfake.Bucket {
	t.Helper()
	bucket, ok := f.Bucket(name)
	require.True(t, ok, "the bucket %s exists", name)
	return bucket
}

func count(calls []string, operation string) int {
	return len(slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return c != operation }))
}

func reasons(actions []action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Reason)
	}
	return out
}

func corrected(actions []action) []string {
	var out []string
	for _, a := range actions {
		if a.Corrected != "" {
			out = append(out, a.Corrected)
		}
	}
	return out
}

func TestEnsureCreatesTheBucketWithItsTagsInOneCall(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("eu-west-1")

	actions, err := b.ensure(context.Background(), ref, testWant(ref), prior{})

	require.NoError(t, err)
	assert.Equal(t, []string{eventReasonBucketCreated, eventReasonLifecycleUpdated}, reasons(actions),
		"a new bucket has no lifecycle rule")
	assert.Empty(t, corrected(actions), "the first create is not drift")
	assert.Equal(t, 1, count(f.Calls(), "CreateBucket"))
	assert.Zero(t, count(f.Calls(), "PutBucketTagging"))
	assert.Equal(t, testWant(ref).Tags, bucketOfFake(t, f, ref.Name).Tags)
	assert.Equal(t, "eu-west-1", bucketOfFake(t, f, ref.Name).Region)
}

func TestEnsureCreatesTheBucketInUSEast1WithoutALocationConstraint(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("us-east-1")

	_, err := b.ensure(context.Background(), ref, testWant(ref), prior{})

	require.NoError(t, err)
	assert.Equal(t, "us-east-1", bucketOfFake(t, f, ref.Name).Region)
}

func TestEnsureSetsSSEKMSWithABucketKey(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("eu-west-1")
	w := testWant(ref)
	w.KeyARN = keyARN

	actions, err := b.ensure(context.Background(), ref, w, prior{})

	require.NoError(t, err)
	encryption := bucketOfFake(t, f, ref.Name).Encryption
	assert.Equal(t, types.ServerSideEncryptionAwsKms, encryption.ApplyServerSideEncryptionByDefault.SSEAlgorithm)
	assert.Equal(t, keyARN, aws.ToString(encryption.ApplyServerSideEncryptionByDefault.KMSMasterKeyID))
	assert.True(t, aws.ToBool(encryption.BucketKeyEnabled))
	assert.Contains(t, reasons(actions), eventReasonEncryptionUpdated)
	assert.Empty(t, corrected(actions))
}

func TestEnsureKeepsSSES3WithoutAKey(t *testing.T) {
	_, f, ref := ensured(t)

	encryption := bucketOfFake(t, f, ref.Name).Encryption
	assert.Equal(t, types.ServerSideEncryptionAes256, encryption.ApplyServerSideEncryptionByDefault.SSEAlgorithm)
	assert.Zero(t, count(f.Calls(), "PutBucketEncryption"), "a new bucket has SSE-S3 already")
}

func TestEnsureChangesTheKeyWithoutDrift(t *testing.T) {
	b, f, ref := ensured(t)
	w := testWant(ref)
	w.KeyARN = keyARN

	actions, err := b.ensure(context.Background(), ref, w, prior{Served: true})

	require.NoError(t, err)
	assert.Equal(t, []string{eventReasonEncryptionUpdated}, reasons(actions))
	assert.Empty(t, corrected(actions), "a changed key input is a change, not drift")
	assert.Equal(t, keyARN, aws.ToString(
		bucketOfFake(t, f, ref.Name).Encryption.ApplyServerSideEncryptionByDefault.KMSMasterKeyID,
	))
}

func TestEnsureRepairsTheEncryptionAsDrift(t *testing.T) {
	b, f, ref := ensured(t)
	w := testWant(ref)
	w.KeyARN = keyARN
	_, err := b.ensure(context.Background(), ref, w, prior{Served: true})
	require.NoError(t, err)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) {
		bucket.Encryption = &types.ServerSideEncryptionRule{
			ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
				SSEAlgorithm: types.ServerSideEncryptionAes256,
			},
		}
	})

	actions, err := b.ensure(context.Background(), ref, w, prior{Served: true, KeyARN: keyARN})

	require.NoError(t, err)
	assert.Len(t, corrected(actions), 1)
}

func TestEnsureAddsTheAbortRule(t *testing.T) {
	b, f, ref := ensured(t)

	rules := bucketOfFake(t, f, ref.Name).LifecycleRules
	require.Len(t, rules, 1)
	assert.Equal(t, abortRuleID, aws.ToString(rules[0].ID))
	assert.Equal(t, types.ExpirationStatusEnabled, rules[0].Status)
	assert.Equal(t, int32(7), aws.ToInt32(rules[0].AbortIncompleteMultipartUpload.DaysAfterInitiation))

	puts := len(f.Calls())
	actions, err := b.ensure(context.Background(), ref, testWant(ref), prior{Served: true})
	require.NoError(t, err)
	assert.Empty(t, actions)
	for _, call := range f.Calls()[puts:] {
		assert.NotContains(t, call, "Put", "a bucket in its desired state gets no write")
	}
}

func TestEnsureKeepsTheLifecycleRulesOfOthers(t *testing.T) {
	b, f, ref := ensured(t)
	logs := types.LifecycleRule{
		ID:         aws.String("logs"),
		Status:     types.ExpirationStatusEnabled,
		Filter:     &types.LifecycleRuleFilter{Prefix: aws.String("logs/")},
		Expiration: &types.LifecycleExpiration{Days: aws.Int32(30)},
	}
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) {
		bucket.LifecycleRules = append([]types.LifecycleRule{logs}, bucket.LifecycleRules...)
	})
	w := testWant(ref)
	w.AbortDays = 3

	_, err := b.ensure(context.Background(), ref, w, prior{Served: true})

	require.NoError(t, err)
	rules := bucketOfFake(t, f, ref.Name).LifecycleRules
	require.Len(t, rules, 2, "the plugin replaces its rule by its ID")
	assert.Equal(t, "logs", aws.ToString(rules[0].ID))
	assert.Equal(t, int32(30), aws.ToInt32(rules[0].Expiration.Days))
	assert.Equal(t, int32(3), aws.ToInt32(rules[1].AbortIncompleteMultipartUpload.DaysAfterInitiation))
}

func TestEnsureAddsAGoneAbortRuleAsDrift(t *testing.T) {
	b, f, ref := ensured(t)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) { bucket.LifecycleRules = nil })

	actions, err := b.ensure(context.Background(), ref, testWant(ref), prior{Served: true})

	require.NoError(t, err)
	assert.Equal(t, []string{eventReasonLifecycleUpdated}, reasons(actions))
	assert.Len(t, corrected(actions), 1)
}

func TestEnsureReadsThePublicAccessBlockAndWritesNothingOnANewBucket(t *testing.T) {
	_, f, _ := ensured(t)

	assert.Equal(t, 1, count(f.Calls(), "GetPublicAccessBlock"))
	assert.Zero(t, count(f.Calls(), "PutPublicAccessBlock"))
}

func TestEnsureBlocksPublicAccessAgainAsDrift(t *testing.T) {
	b, f, ref := ensured(t)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) {
		bucket.PublicAccessBlock.BlockPublicPolicy = aws.Bool(false)
	})

	actions, err := b.ensure(context.Background(), ref, testWant(ref), prior{Served: true})

	require.NoError(t, err)
	assert.Equal(t, []string{eventReasonPublicAccessBlocked}, reasons(actions))
	assert.Len(t, corrected(actions), 1)
	assert.True(t, aws.ToBool(bucketOfFake(t, f, ref.Name).PublicAccessBlock.BlockPublicPolicy))
}

func TestEnsureBlocksAMissingPublicAccessBlock(t *testing.T) {
	b, f, ref := ensured(t)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) { bucket.PublicAccessBlock = nil })

	_, err := b.ensure(context.Background(), ref, testWant(ref), prior{Served: true})

	require.NoError(t, err)
	block := bucketOfFake(t, f, ref.Name).PublicAccessBlock
	require.NotNil(t, block)
	assert.True(t, aws.ToBool(block.BlockPublicAcls) && aws.ToBool(block.IgnorePublicAcls) &&
		aws.ToBool(block.BlockPublicPolicy) && aws.ToBool(block.RestrictPublicBuckets))
}

func TestEnsureMergesTheTagsByKey(t *testing.T) {
	b, f, ref := ensured(t)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) {
		bucket.Tags["owner"] = "finance"
		bucket.Tags["team"] = "someone-else"
	})

	actions, err := b.ensure(context.Background(), ref, testWant(ref), prior{Served: true})

	require.NoError(t, err)
	assert.Equal(t, []string{eventReasonBucketTagsUpdated}, reasons(actions))
	assert.Empty(t, corrected(actions))
	tags := bucketOfFake(t, f, ref.Name).Tags
	assert.Equal(t, "finance", tags["owner"], "the plugin keeps the tag of another key")
	assert.Equal(t, "platform", tags["team"])
	assert.Len(t, tags, 6)
}

func TestEnsureRefusesABucketWithAnotherUID(t *testing.T) {
	b, f, ref := ensured(t)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) { bucket.Tags[awskit.TagRequestUID] = otherUID })
	before := bucketOfFake(t, f, ref.Name)

	_, err := b.ensure(context.Background(), ref, testWant(ref), prior{})

	var inUse bucketInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Contains(t, err.Error(), ref.Name)
	assert.Contains(t, err.Error(), "the request team-a/orders-backup with the UID "+otherUID)
	assert.Equal(t, before, bucketOfFake(t, f, ref.Name), "the plugin changes nothing on the bucket")
}

func TestEnsureRefusesABucketWithoutTags(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("eu-west-1")
	_, err := f.In("eu-west-1").CreateBucket(context.Background(), createInput(ref.Name, "eu-west-1"))
	require.NoError(t, err)

	_, err = b.ensure(context.Background(), ref, testWant(ref), prior{})

	var inUse bucketInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "no request", inUse.Owner)
}

func TestEnsureRefusesABucketOfAnotherAccount(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("eu-west-1")
	f.AddForeignBucket(ref.Name, "eu-west-1")

	_, err := b.ensure(context.Background(), ref, testWant(ref), prior{})

	var inUse bucketInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "another AWS account", inUse.Owner)
}

func TestEnsureRefusesABucketOfTheNameInAnotherRegion(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("eu-west-1")
	_, err := f.In("eu-central-1").CreateBucket(context.Background(), createInput(ref.Name, "eu-central-1"))
	require.NoError(t, err)

	_, err = b.ensure(context.Background(), ref, testWant(ref), prior{})

	var inUse bucketInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "a bucket of the same name in another region", inUse.Owner)
}

func TestEnsureReadsTheTagsAfterACreateThatAnsweredSuccess(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("us-east-1")
	other := ref
	other.UID = otherUID
	_, err := b.ensure(context.Background(), other, testWant(other), prior{})
	require.NoError(t, err)
	before := bucketOfFake(t, f, ref.Name)
	// The head of a bucket that S3 just created can still miss it.
	f.FailNext("HeadBucket", &types.NotFound{})

	actions, err := b.ensure(context.Background(), ref, testWant(ref), prior{})

	var inUse bucketInUseError
	require.ErrorAs(t, err, &inUse, "in us-east-1 a create of an owned name answers success")
	assert.Contains(t, inUse.Owner, otherUID)
	assert.Empty(t, actions, "no create is reported")
	assert.Equal(t, before, bucketOfFake(t, f, ref.Name))
}

func TestEnsureCreatesAGoneBucketAgainAsDrift(t *testing.T) {
	b, _ := newBuckets()
	ref := testRef("eu-west-1")

	actions, err := b.ensure(context.Background(), ref, testWant(ref), prior{Served: true})

	require.NoError(t, err)
	assert.Equal(t, []string{"created the bucket " + ref.Name + ", which was gone"}, corrected(actions))
}

func TestEnsureReturnsAnErrorOfS3(t *testing.T) {
	b, f := newBuckets()
	boom := errors.New("boom")
	f.FailNext("CreateBucket", boom)
	ref := testRef("eu-west-1")

	_, err := b.ensure(context.Background(), ref, testWant(ref), prior{})

	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), ref.Name)
}

// fill puts n objects into the bucket name.
func fill(f *awsfake.S3, name string, n int) {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("backup/%05d", i)
	}
	f.PutObjects(name, keys...)
}

func TestDeleteEmptiesAndDeletesTheBucket(t *testing.T) {
	b, f, ref := ensured(t)
	fill(f, ref.Name, 2500)

	actions, remain, err := b.delete(context.Background(), ref)

	require.NoError(t, err)
	assert.False(t, remain)
	assert.Equal(t, []string{eventReasonBucketDeleted}, reasons(actions))
	_, ok := f.Bucket(ref.Name)
	assert.False(t, ok)
}

func TestDeleteNeedsMorePassesForABucketLargerThanTheBudget(t *testing.T) {
	b, f, ref := ensured(t)
	b.deleteBudget = time.Nanosecond
	fill(f, ref.Name, 2500)

	passes := 0
	for remain := true; remain; passes++ {
		require.Less(t, passes, 10)
		var err error
		var actions []action
		actions, remain, err = b.delete(context.Background(), ref)
		require.NoError(t, err)
		if remain {
			assert.Empty(t, actions)
			assert.Zero(t, count(f.Calls(), "DeleteBucket"), "a pass that stops early deletes no bucket")
			_, ok := f.Bucket(ref.Name)
			assert.True(t, ok)
		}
	}

	assert.Equal(t, 3, passes)
	_, ok := f.Bucket(ref.Name)
	assert.False(t, ok)
}

func TestDeleteGoesOnAfterARestart(t *testing.T) {
	b, f, ref := ensured(t)
	b.deleteBudget = time.Nanosecond
	fill(f, ref.Name, 1500)
	_, remain, err := b.delete(context.Background(), ref)
	require.NoError(t, err)
	require.True(t, remain)

	// A new plugin process knows nothing of the pass before.
	restarted := buckets{s3: f.In, deleteBudget: time.Minute}
	_, remain, err = restarted.delete(context.Background(), ref)

	require.NoError(t, err)
	assert.False(t, remain)
	_, ok := f.Bucket(ref.Name)
	assert.False(t, ok)
}

func TestDeleteDeletesTheVersionsOfABucketWithVersioning(t *testing.T) {
	b, f, ref := ensured(t)
	fill(f, ref.Name, 10)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) {
		bucket.Versioning = types.BucketVersioningStatusEnabled
		bucket.Versions = []awsfake.ObjectVersion{
			{Key: "backup/00001", VersionID: "v1"},
			{Key: "backup/old", VersionID: "v2", DeleteMarker: true},
		}
	})

	_, remain, err := b.delete(context.Background(), ref)

	require.NoError(t, err)
	assert.False(t, remain)
	_, ok := f.Bucket(ref.Name)
	assert.False(t, ok)
}

func TestDeleteGoesOnWhenAWriterAddedObjects(t *testing.T) {
	b, f, ref := ensured(t)
	f.FailNext("DeleteBucket", &smithy.GenericAPIError{Code: "BucketNotEmpty"})

	_, remain, err := b.delete(context.Background(), ref)

	require.NoError(t, err)
	assert.True(t, remain)
}

func TestDeleteOfAMissingBucketIsDone(t *testing.T) {
	b, _ := newBuckets()

	actions, remain, err := b.delete(context.Background(), testRef("eu-west-1"))

	require.NoError(t, err)
	assert.False(t, remain)
	assert.Empty(t, actions)
}

func TestDeleteKeepsABucketOfAnotherRequest(t *testing.T) {
	b, f, ref := ensured(t)
	fill(f, ref.Name, 3)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) { bucket.Tags[awskit.TagRequestUID] = otherUID })

	actions, remain, err := b.delete(context.Background(), ref)

	require.NoError(t, err)
	assert.False(t, remain)
	assert.Empty(t, actions)
	assert.Len(t, bucketOfFake(t, f, ref.Name).Objects, 3)
}

func TestDeleteOfABucketOfAnotherAccountIsDoneOnlyWithoutARecord(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("eu-west-1")
	f.AddForeignBucket(ref.Name, "eu-west-1")

	_, _, err := b.delete(context.Background(), ref)
	require.NoError(t, err, "the request never had the bucket")

	ref.Recorded = true
	_, _, err = b.delete(context.Background(), ref)
	assert.Error(t, err, "the request had the bucket, so a refused read is a fault")
}

func TestReleaseRemovesOnlyTheTagsOfTheRequest(t *testing.T) {
	b, f, ref := ensured(t)
	w := testWant(ref)
	w.KeyARN = keyARN
	_, err := b.ensure(context.Background(), ref, w, prior{})
	require.NoError(t, err)
	fill(f, ref.Name, 3)

	actions, err := b.release(context.Background(), ref)

	require.NoError(t, err)
	assert.Equal(t, []string{eventReasonBucketReleased}, reasons(actions))
	bucket := bucketOfFake(t, f, ref.Name)
	assert.Equal(t, map[string]string{"team": "platform"}, bucket.Tags)
	assert.Len(t, bucket.Objects, 3)
	assert.Equal(t, keyARN, aws.ToString(bucket.Encryption.ApplyServerSideEncryptionByDefault.KMSMasterKeyID))
}

func TestReleaseDeletesTheTagSetWhenNoTagStays(t *testing.T) {
	b, f := newBuckets()
	ref := testRef("eu-west-1")
	_, err := b.ensure(context.Background(), ref, want{Tags: bucketTags(Settings{}, ref), AbortDays: 7}, prior{})
	require.NoError(t, err)

	_, err = b.release(context.Background(), ref)

	require.NoError(t, err)
	assert.Equal(t, 1, count(f.Calls(), "DeleteBucketTagging"))
	assert.Empty(t, bucketOfFake(t, f, ref.Name).Tags)
}

func TestReleaseKeepsABucketOfAnotherRequest(t *testing.T) {
	b, f, ref := ensured(t)
	f.Edit(ref.Name, func(bucket *awsfake.Bucket) { bucket.Tags[awskit.TagRequestUID] = otherUID })

	actions, err := b.release(context.Background(), ref)

	require.NoError(t, err)
	assert.Empty(t, actions)
	assert.Equal(t, otherUID, bucketOfFake(t, f, ref.Name).Tags[awskit.TagRequestUID])
}
