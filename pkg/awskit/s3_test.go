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
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

// newS3 returns a fake S3 with the empty bucket bucket-1 in its home region.
func newS3(t *testing.T) *awsfake.S3 {
	f := awsfake.NewS3()
	_, err := f.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket:                    aws.String("bucket-1"),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: "eu-west-1"},
	})
	require.NoError(t, err)
	return f
}

// count returns how often operation is in calls.
func count(calls []string, operation string) int {
	return len(slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return c != operation }))
}

func TestEmptyBucketDeletesInBatchesOf1000(t *testing.T) {
	f := newS3(t)
	keys := make([]string, 2500)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%04d", i)
	}
	f.PutObjects("bucket-1", keys...)

	require.NoError(t, awskit.EmptyBucket(context.Background(), f, "bucket-1"))

	b, _ := f.Bucket("bucket-1")
	assert.Empty(t, b.Objects)
	assert.Equal(t, 3, count(f.Calls(), "DeleteObjects"))
}

func TestEmptyBucketNamesARefusedKey(t *testing.T) {
	f := newS3(t)
	f.PutObjects("bucket-1", "k1", "k2")
	f.RefuseDelete("bucket-1", "k1", "AccessDenied")

	err := awskit.EmptyBucket(context.Background(), f, "bucket-1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "k1: AccessDenied")
	b, _ := f.Bucket("bucket-1")
	assert.Equal(t, []string{"k1"}, b.Objects)
}

func TestEmptyBucketOfAnEmptyBucketDeletesNothing(t *testing.T) {
	f := newS3(t)

	require.NoError(t, awskit.EmptyBucket(context.Background(), f, "bucket-1"))

	assert.Zero(t, count(f.Calls(), "DeleteObjects"))
}

func TestEmptyBucketReturnsAnErrorOfS3(t *testing.T) {
	f := newS3(t)
	f.PutObjects("bucket-1", "k1")
	boom := errors.New("boom")
	f.FailNext("DeleteObjects", boom)

	err := awskit.EmptyBucket(context.Background(), f, "bucket-1")

	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), `"bucket-1"`)
}

// putKeys puts n objects into bucket-1 of f.
func putKeys(f *awsfake.S3, n int) {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%04d", i)
	}
	f.PutObjects("bucket-1", keys...)
}

func deleteBucket(f *awsfake.S3) error {
	_, err := f.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String("bucket-1")})
	return err
}

func TestEmptyBucketWithinEmptiesABucketThatFitsTheBudget(t *testing.T) {
	f := newS3(t)
	putKeys(f, 2500)

	remain, err := awskit.EmptyBucketWithin(context.Background(), f, "bucket-1", time.Minute)

	require.NoError(t, err)
	assert.False(t, remain)
	require.NoError(t, deleteBucket(f))
}

func TestEmptyBucketWithinStopsAtTheEndOfTheBudget(t *testing.T) {
	f := newS3(t)
	putKeys(f, 2500)

	remain, err := awskit.EmptyBucketWithin(context.Background(), f, "bucket-1", 0)

	require.NoError(t, err)
	assert.True(t, remain)
	b, _ := f.Bucket("bucket-1")
	assert.Len(t, b.Objects, 1500, "a budget that ended deletes one batch")
}

func TestEmptyBucketWithinGoesOnInALaterCall(t *testing.T) {
	f := newS3(t)
	putKeys(f, 2500)

	calls := 0
	for remain := true; remain; calls++ {
		require.Less(t, calls, 10)
		var err error
		remain, err = awskit.EmptyBucketWithin(context.Background(), f, "bucket-1", 0)
		require.NoError(t, err)
	}

	assert.Equal(t, 3, calls)
	require.NoError(t, deleteBucket(f))
}

func TestEmptyBucketWithinDeletesEachVersionOfAVersionedBucket(t *testing.T) {
	for _, status := range []types.BucketVersioningStatus{
		types.BucketVersioningStatusEnabled,
		types.BucketVersioningStatusSuspended,
	} {
		t.Run(string(status), func(t *testing.T) {
			f := newS3(t)
			putKeys(f, 1200)
			f.Edit("bucket-1", func(b *awsfake.Bucket) {
				b.Versioning = status
				b.Versions = []awsfake.ObjectVersion{
					{Key: "k0001", VersionID: "v1"},
					{Key: "gone", VersionID: "v2", DeleteMarker: true},
				}
			})

			remain, err := awskit.EmptyBucketWithin(context.Background(), f, "bucket-1", time.Minute)

			require.NoError(t, err)
			assert.False(t, remain)
			require.NoError(t, deleteBucket(f), "no version and no delete marker stays")
		})
	}
}

func TestEmptyBucketLeavesTheVersionsOfAVersionedBucket(t *testing.T) {
	f := newS3(t)
	putKeys(f, 3)
	f.Edit("bucket-1", func(b *awsfake.Bucket) { b.Versioning = types.BucketVersioningStatusEnabled })

	require.NoError(t, awskit.EmptyBucket(context.Background(), f, "bucket-1"))

	assert.Equal(t, "BucketNotEmpty", awskit.ErrorCode(deleteBucket(f)))
}

func TestEmptyBucketWithinNamesARefusedKey(t *testing.T) {
	f := newS3(t)
	f.PutObjects("bucket-1", "k1", "k2")
	f.RefuseDelete("bucket-1", "k1", "AccessDenied")

	_, err := awskit.EmptyBucketWithin(context.Background(), f, "bucket-1", time.Minute)

	assert.ErrorContains(t, err, "k1: AccessDenied")
}

func TestEmptyBucketWithinOfAnEmptyBucketDeletesNothing(t *testing.T) {
	f := newS3(t)

	remain, err := awskit.EmptyBucketWithin(context.Background(), f, "bucket-1", time.Minute)

	require.NoError(t, err)
	assert.False(t, remain)
	assert.Zero(t, count(f.Calls(), "DeleteObjects"))
}

func TestEmptyBucketWithinReturnsAnErrorOfS3(t *testing.T) {
	f := newS3(t)
	boom := errors.New("boom")
	f.FailNext("GetBucketVersioning", boom)

	_, err := awskit.EmptyBucketWithin(context.Background(), f, "bucket-1", time.Minute)

	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), `"bucket-1"`)
}
