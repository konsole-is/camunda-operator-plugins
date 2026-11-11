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
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

// newS3 returns a fake S3 with the buckets, and how many calls it took to
// create them. Its home is us-east-1, the one region where S3 takes a
// CreateBucket without a location constraint, as the bucket client sends it.
func newS3(t *testing.T, buckets ...string) (*awsfake.S3, int) {
	f := awsfake.NewS3()
	f.Home = "us-east-1"
	for _, b := range buckets {
		_, err := f.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(b)})
		require.NoError(t, err)
	}
	return f, len(f.Calls())
}

func TestEnsureBucketCreatesAMissingBucket(t *testing.T) {
	f, _ := newS3(t)
	created, err := (&bucketClient{api: f}).EnsureBucket(context.Background(), "bucket-1")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Len(t, slices.DeleteFunc(f.Calls(), func(c string) bool { return c != "CreateBucket" }), 1)
	_, ok := f.Bucket("bucket-1")
	assert.True(t, ok)
}

func TestEnsureBucketKeepsAnExistingBucket(t *testing.T) {
	f, setup := newS3(t, "bucket-1")
	created, err := (&bucketClient{api: f}).EnsureBucket(context.Background(), "bucket-1")
	require.NoError(t, err)
	assert.False(t, created)
	assert.NotContains(t, f.Calls()[setup:], "CreateBucket")
}

func TestEnsureBucketAcceptsAnOwnedBucket(t *testing.T) {
	f, _ := newS3(t)
	f.FailNext("CreateBucket", &types.BucketAlreadyOwnedByYou{})
	created, err := (&bucketClient{api: f}).EnsureBucket(context.Background(), "bucket-1")
	require.NoError(t, err)
	assert.False(t, created)
}

func TestEnsureBucketRefusesABucketOfAnotherUser(t *testing.T) {
	f, _ := newS3(t)
	f.FailNext("CreateBucket", &types.BucketAlreadyExists{})
	_, err := (&bucketClient{api: f}).EnsureBucket(context.Background(), "bucket-1")
	assert.ErrorContains(t, err, `bucket "bucket-1" exists and belongs to another RustFS user`)
}

func TestDeleteBucketEmptiesAndDeletesIt(t *testing.T) {
	f, _ := newS3(t, "bucket-1")
	f.PutObjects("bucket-1", "k1")
	deleted, err := (&bucketClient{api: f}).DeleteBucket(context.Background(), "bucket-1")
	require.NoError(t, err)
	assert.True(t, deleted)
	_, ok := f.Bucket("bucket-1")
	assert.False(t, ok)
}

func TestDeleteBucketOfAMissingBucket(t *testing.T) {
	f, _ := newS3(t)
	deleted, err := (&bucketClient{api: f}).DeleteBucket(context.Background(), "bucket-1")
	require.NoError(t, err)
	assert.False(t, deleted)
	assert.NotContains(t, f.Calls(), "DeleteBucket")
}

func TestNewBucketClientBoundsEachCall(t *testing.T) {
	c := newBucketClient("http://rustfs:9000", "us-east-1", "admin", "admin-secret")
	client, ok := c.api.(*s3.Client)
	require.True(t, ok)
	httpClient, ok := client.Options().HTTPClient.(*awshttp.BuildableClient)
	require.True(t, ok, "the S3 client has a timeout of its own")
	assert.Equal(t, callTimeout, httpClient.GetTimeout())
}
