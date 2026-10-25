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

package rustfs

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeS3 is an S3 API that holds the keys of each bucket in memory.
type fakeS3 struct {
	buckets     map[string][]string
	createErr   error
	deleteErrs  []types.Error
	createCalls int
	deleteCalls int
	batches     []int
}

func (f *fakeS3) HeadBucket(
	_ context.Context,
	in *s3.HeadBucketInput,
	_ ...func(*s3.Options),
) (*s3.HeadBucketOutput, error) {
	if _, ok := f.buckets[*in.Bucket]; !ok {
		return nil, &types.NotFound{}
	}
	return &s3.HeadBucketOutput{}, nil
}

func (f *fakeS3) CreateBucket(
	_ context.Context,
	in *s3.CreateBucketInput,
	_ ...func(*s3.Options),
) (*s3.CreateBucketOutput, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.buckets[*in.Bucket] = nil
	return &s3.CreateBucketOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(
	_ context.Context,
	in *s3.ListObjectsV2Input,
	_ ...func(*s3.Options),
) (*s3.ListObjectsV2Output, error) {
	keys := f.buckets[*in.Bucket]
	n := min(len(keys), int(aws.ToInt32(in.MaxKeys)))
	out := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(n < len(keys))}
	for _, k := range keys[:n] {
		out.Contents = append(out.Contents, types.Object{Key: aws.String(k)})
	}
	return out, nil
}

func (f *fakeS3) DeleteObjects(
	_ context.Context,
	in *s3.DeleteObjectsInput,
	_ ...func(*s3.Options),
) (*s3.DeleteObjectsOutput, error) {
	f.batches = append(f.batches, len(in.Delete.Objects))
	if f.deleteErrs != nil {
		return &s3.DeleteObjectsOutput{Errors: f.deleteErrs}, nil
	}
	f.buckets[*in.Bucket] = slices.DeleteFunc(f.buckets[*in.Bucket], func(k string) bool {
		return slices.ContainsFunc(in.Delete.Objects, func(o types.ObjectIdentifier) bool { return *o.Key == k })
	})
	return &s3.DeleteObjectsOutput{}, nil
}

func (f *fakeS3) DeleteBucket(
	_ context.Context,
	in *s3.DeleteBucketInput,
	_ ...func(*s3.Options),
) (*s3.DeleteBucketOutput, error) {
	f.deleteCalls++
	if len(f.buckets[*in.Bucket]) > 0 {
		return nil, fmt.Errorf("bucket %s is not empty", *in.Bucket)
	}
	delete(f.buckets, *in.Bucket)
	return &s3.DeleteBucketOutput{}, nil
}

func newFakeS3(buckets ...string) *fakeS3 {
	f := &fakeS3{buckets: map[string][]string{}}
	for _, b := range buckets {
		f.buckets[b] = nil
	}
	return f
}

func TestEnsureBucketCreatesAMissingBucket(t *testing.T) {
	f := newFakeS3()
	created, err := (&bucketClient{api: f}).EnsureBucket(context.Background(), "b1")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 1, f.createCalls)
	assert.Contains(t, f.buckets, "b1")
}

func TestEnsureBucketKeepsAnExistingBucket(t *testing.T) {
	f := newFakeS3("b1")
	created, err := (&bucketClient{api: f}).EnsureBucket(context.Background(), "b1")
	require.NoError(t, err)
	assert.False(t, created)
	assert.Zero(t, f.createCalls)
}

func TestEnsureBucketAcceptsAnOwnedBucket(t *testing.T) {
	f := newFakeS3()
	f.createErr = &types.BucketAlreadyOwnedByYou{}
	created, err := (&bucketClient{api: f}).EnsureBucket(context.Background(), "b1")
	require.NoError(t, err)
	assert.False(t, created)
}

func TestEnsureBucketRefusesABucketOfAnotherUser(t *testing.T) {
	f := newFakeS3()
	f.createErr = &types.BucketAlreadyExists{}
	_, err := (&bucketClient{api: f}).EnsureBucket(context.Background(), "b1")
	assert.ErrorContains(t, err, `bucket "b1" exists and belongs to another RustFS user`)
}

func TestEmptyBucketDeletesInBatches(t *testing.T) {
	f := newFakeS3()
	for i := range 2500 {
		f.buckets["b1"] = append(f.buckets["b1"], fmt.Sprintf("k%d", i))
	}

	require.NoError(t, emptyBucket(context.Background(), f, "b1"))
	assert.Equal(t, []int{1000, 1000, 500}, f.batches)
	assert.Empty(t, f.buckets["b1"])
}

func TestEmptyBucketFailsOnAPartialDelete(t *testing.T) {
	f := newFakeS3()
	f.buckets["b1"] = []string{"k1", "k2"}
	f.deleteErrs = []types.Error{{Key: aws.String("k1"), Code: aws.String("AccessDenied")}}

	err := emptyBucket(context.Background(), f, "b1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "k1")
	assert.Contains(t, err.Error(), "AccessDenied")
}

func TestDeleteBucketEmptiesAndDeletesIt(t *testing.T) {
	f := newFakeS3()
	f.buckets["b1"] = []string{"k1"}
	deleted, err := (&bucketClient{api: f}).DeleteBucket(context.Background(), "b1")
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.NotContains(t, f.buckets, "b1")
}

func TestDeleteBucketOfAMissingBucket(t *testing.T) {
	f := newFakeS3()
	deleted, err := (&bucketClient{api: f}).DeleteBucket(context.Background(), "b1")
	require.NoError(t, err)
	assert.False(t, deleted)
	assert.Zero(t, f.deleteCalls)
}
