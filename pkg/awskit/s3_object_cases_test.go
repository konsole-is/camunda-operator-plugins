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

// The fake keeps each current object as the version null. So each versioning
// case puts its objects before it turns versioning on or suspends it: AWS
// then holds them as null versions too.

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

var s3ObjectCases = []awsCase{
	{"lists objects in pages of at most 1,000 keys", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := a.bucket(t, "s3-pages", nil)
		keys := make([]string, 2500)
		for i := range keys {
			keys[i] = fmt.Sprintf("k%04d", len(keys)-1-i)
		}
		a.putObjects(t, bucket, keys...)

		a.eventually(t, func(c *assert.CollectT) {
			var listed []string
			var pages []int
			var token *string
			for {
				out, err := a.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
					Bucket:            aws.String(bucket),
					ContinuationToken: token,
				})
				require.NoError(c, err)
				pages = append(pages, len(out.Contents))
				for _, o := range out.Contents {
					listed = append(listed, aws.ToString(o.Key))
				}
				if !aws.ToBool(out.IsTruncated) {
					assert.Nil(c, out.NextContinuationToken)
					break
				}
				token = out.NextContinuationToken
			}

			assert.Equal(c, []int{1000, 1000, 500}, pages, "1,000 keys is the default and the most")
			assert.Len(c, listed, 2500)
			assert.IsIncreasing(c, listed, "keys come in order")
		})
		out, err := a.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(bucket),
			MaxKeys: aws.Int32(5000),
		})
		require.NoError(t, err)
		assert.Len(t, out.Contents, 1000)
	}},
	{"lists no object with MaxKeys zero", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-zero-keys", nil)
		a.putObjects(t, bucket, "k1", "k2")

		out, err := a.s3.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
			Bucket:  aws.String(bucket),
			MaxKeys: aws.Int32(0),
		})

		require.NoError(t, err)
		assert.Empty(t, out.Contents)
		assert.False(t, aws.ToBool(out.IsTruncated))
	}},
	{"refuses a negative MaxKeys", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-negative-keys", nil)
		a.putObjects(t, bucket, "k1")

		_, err := a.s3.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
			Bucket:  aws.String(bucket),
			MaxKeys: aws.Int32(-1),
		})

		assert.Equal(t, "InvalidArgument", awskit.ErrorCode(err), "ListObjectsV2: %v", err)
	}},
	{"refuses more than 1,000 keys in one delete", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-big-delete", nil)
		objects := make([]s3types.ObjectIdentifier, 1001)
		for i := range objects {
			objects[i] = s3types.ObjectIdentifier{Key: aws.String(fmt.Sprintf("k%d", i))}
		}

		_, err := a.s3.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &s3types.Delete{Objects: objects},
		})

		assert.Equal(t, "MalformedXML", awskit.ErrorCode(err), "DeleteObjects: %v", err)
	}},
	{"refuses to delete a bucket with objects", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := a.bucket(t, "s3-not-empty", nil)
		a.putObjects(t, bucket, "k1")

		_, err := a.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
		assert.Equal(t, "BucketNotEmpty", awskit.ErrorCode(err), "DeleteBucket: %v", err)

		require.NoError(t, awskit.EmptyBucket(ctx, a.s3, bucket))
		_, err = a.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
		require.NoError(t, err)
		a.eventually(t, func(c *assert.CollectT) {
			_, err := a.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
			assert.ErrorAs(c, err, new(*s3types.NotFound))
		})
	}},
	{"gives the versioning of a bucket", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := a.bucket(t, "s3-versioning", nil)

		out, err := a.s3.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
		require.NoError(t, err)
		assert.Empty(t, out.Status, "a bucket that never had versioning on has no status")

		a.setVersioning(t, bucket, s3types.BucketVersioningStatusSuspended)
		a.eventually(t, func(c *assert.CollectT) {
			out, err := a.s3.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
			require.NoError(c, err)
			assert.Equal(c, s3types.BucketVersioningStatusSuspended, out.Status)
		})

		_, err = a.s3.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
			Bucket: aws.String(a.name("s3-versioning-missing")),
		})
		assert.True(t, awskit.IsNotFound(err), "GetBucketVersioning of a missing bucket: %v", err)
	}},
	{"keeps the version and adds a delete marker when versioning is on", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-marker", nil)
		a.putObjects(t, bucket, "k1", "k2")
		a.setVersioning(t, bucket, s3types.BucketVersioningStatusEnabled)

		out := deleteKeys(t, a, bucket, s3types.ObjectIdentifier{Key: aws.String("k1")})

		require.Len(t, out.Deleted, 1)
		assert.True(t, aws.ToBool(out.Deleted[0].DeleteMarker))
		a.eventually(t, func(c *assert.CollectT) {
			assert.Equal(c, []string{"k2"}, listKeys(c, a, bucket), "the key is gone from ListObjectsV2")
			versions, markers := listVersions(c, a, bucket)
			assert.Equal(c, []listedObject{{"k1", "null", false}, {"k2", "null", true}}, versions)
			require.Len(c, markers, 1)
			assert.Equal(c, "k1", markers[0].key)
			assert.True(c, markers[0].latest)
			assert.NotEqual(c, "null", markers[0].version)
		})
	}},
	{"replaces the null version with a delete marker when versioning is suspended", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-suspended", nil)
		a.putObjects(t, bucket, "k1", "k2")
		a.setVersioning(t, bucket, s3types.BucketVersioningStatusSuspended)

		deleteKeys(t, a, bucket, s3types.ObjectIdentifier{Key: aws.String("k1")})

		a.eventually(t, func(c *assert.CollectT) {
			versions, markers := listVersions(c, a, bucket)
			assert.Equal(c, []listedObject{{"k2", "null", true}}, versions)
			assert.Equal(c, []listedObject{{"k1", "null", true}}, markers)
		})
	}},
	{"deletes a version for good", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-for-good", nil)
		a.putObjects(t, bucket, "k1", "k2")
		a.setVersioning(t, bucket, s3types.BucketVersioningStatusEnabled)
		out := deleteKeys(t, a, bucket, s3types.ObjectIdentifier{Key: aws.String("k1")})
		require.Len(t, out.Deleted, 1)
		marker := out.Deleted[0].DeleteMarkerVersionId
		require.NotNil(t, marker)

		deleteKeys(
			t,
			a,
			bucket,
			s3types.ObjectIdentifier{Key: aws.String("k1"), VersionId: aws.String("null")},
			s3types.ObjectIdentifier{Key: aws.String("k1"), VersionId: marker},
			s3types.ObjectIdentifier{Key: aws.String("k2"), VersionId: aws.String("null")},
		)

		a.eventually(t, func(c *assert.CollectT) {
			assert.Empty(c, listKeys(c, a, bucket))
			versions, markers := listVersions(c, a, bucket)
			assert.Empty(c, versions)
			assert.Empty(c, markers, "a delete with a version ID adds no delete marker")
		})
	}},
	{"lists each version and delete marker in pages", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-version-pages", nil)
		a.putObjects(t, bucket, "k1", "k2", "k3")
		a.setVersioning(t, bucket, s3types.BucketVersioningStatusEnabled)
		deleteKeys(
			t,
			a,
			bucket,
			s3types.ObjectIdentifier{Key: aws.String("k1")},
			s3types.ObjectIdentifier{Key: aws.String("k3")},
		)

		a.eventually(t, func(c *assert.CollectT) {
			var versions, markers []string
			in := &s3.ListObjectVersionsInput{Bucket: aws.String(bucket), MaxKeys: aws.Int32(2)}
			pages := 0
			for {
				out, err := a.s3.ListObjectVersions(context.Background(), in)
				require.NoError(c, err)
				pages++
				for _, v := range out.Versions {
					versions = append(versions, aws.ToString(v.Key)+"@"+aws.ToString(v.VersionId))
				}
				for _, m := range out.DeleteMarkers {
					markers = append(markers, aws.ToString(m.Key))
				}
				if !aws.ToBool(out.IsTruncated) || pages > 3 {
					break
				}
				in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
			}

			assert.Equal(c, 3, pages)
			assert.Equal(c, []string{"k1@null", "k2@null", "k3@null"}, versions)
			assert.Equal(c, []string{"k1", "k3"}, markers)
		})
	}},
	{"refuses to delete a bucket with versions", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-versions-left", nil)
		a.putObjects(t, bucket, "k1", "k2")
		a.setVersioning(t, bucket, s3types.BucketVersioningStatusEnabled)
		deleteKeys(
			t,
			a,
			bucket,
			s3types.ObjectIdentifier{Key: aws.String("k1")},
			s3types.ObjectIdentifier{Key: aws.String("k2")},
		)

		_, err := a.s3.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})

		assert.Equal(t, "BucketNotEmpty", awskit.ErrorCode(err), "DeleteBucket: %v", err)
	}},
}

// listedObject is a version or a delete marker of ListObjectVersions.
type listedObject struct {
	key, version string
	latest       bool
}

func deleteKeys(t *testing.T, a account, bucket string, ids ...s3types.ObjectIdentifier) *s3.DeleteObjectsOutput {
	t.Helper()
	out, err := a.s3.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &s3types.Delete{Objects: ids},
	})
	require.NoError(t, err)
	require.Empty(t, out.Errors)
	return out
}

func listKeys(c *assert.CollectT, a account, bucket string) []string {
	out, err := a.s3.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	require.NoError(c, err)
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	return keys
}

// listVersions returns the versions and the delete markers of the first page
// of ListObjectVersions on bucket, each in the order of the page.
func listVersions(c *assert.CollectT, a account, bucket string) (versions, markers []listedObject) {
	out, err := a.s3.ListObjectVersions(context.Background(), &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
	require.NoError(c, err)
	for _, v := range out.Versions {
		versions = append(
			versions,
			listedObject{aws.ToString(v.Key), aws.ToString(v.VersionId), aws.ToBool(v.IsLatest)},
		)
	}
	for _, m := range out.DeleteMarkers {
		markers = append(markers, listedObject{aws.ToString(m.Key), aws.ToString(m.VersionId), aws.ToBool(m.IsLatest)})
	}
	return versions, markers
}
