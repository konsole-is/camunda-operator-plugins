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

package awskit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// deleteBatch is the most keys that one DeleteObjects call takes.
const deleteBatch = 1000

// EmptyBucket deletes every object of bucket, at most 1,000 keys with each
// DeleteObjects call. A key that S3 refuses to delete is an error that names
// the key and its code. It deletes no object version and no delete marker,
// so a bucket with versioning keeps them, and its delete fails.
func EmptyBucket(ctx context.Context, c S3, bucket string) error {
	for {
		page, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(bucket),
			MaxKeys: aws.Int32(deleteBatch),
		})
		if err != nil {
			return fmt.Errorf("listing the objects of the bucket %q: %w", bucket, err)
		}

		if len(page.Contents) == 0 {
			return nil
		}

		objects := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, o := range page.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: o.Key})
		}
		if err := deleteObjects(ctx, c, bucket, objects); err != nil {
			return err
		}
	}
}

// EmptyBucketWithin deletes the objects of bucket until the bucket is empty
// or budget has passed, and reports whether objects remain. It deletes at
// least one batch of at most 1,000 keys, so each call makes progress, and a
// later call goes on where it stopped. A call after the first batch that is
// still running when the budget ends stops, and objects remain. When the
// versioning of the bucket is on or suspended, it deletes each object version
// and each delete marker too, so that the bucket can be deleted. A key that
// S3 refuses to delete is an error that names the key and its code.
func EmptyBucketWithin(ctx context.Context, c S3, bucket string, budget time.Duration) (bool, error) {
	deadline := time.Now().Add(budget)

	out, err := c.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	if err != nil {
		return false, fmt.Errorf("reading the versioning of the bucket %q: %w", bucket, err)
	}
	deletePage := deleteObjectPage
	if out.Status != "" {
		deletePage = deleteVersionPage
	}

	remain, err := deletePage(ctx, c, bucket)
	for err == nil && remain && time.Now().Before(deadline) {
		remain, err = deletePageUntil(ctx, deadline, deletePage, c, bucket)
	}
	return remain, err
}

// deletePageUntil is deletePage with its calls bounded by deadline. A call
// that the deadline ends is no error, and objects remain.
func deletePageUntil(
	ctx context.Context,
	deadline time.Time,
	deletePage func(context.Context, S3, string) (bool, error),
	c S3,
	bucket string,
) (bool, error) {
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	remain, err := deletePage(bounded, c, bucket)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return true, nil
	}
	return remain, err
}

// deleteObjectPage deletes the first page of objects of bucket, and reports
// whether more objects remain.
func deleteObjectPage(ctx context.Context, c S3, bucket string) (bool, error) {
	page, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		MaxKeys: aws.Int32(deleteBatch),
	})
	if err != nil {
		return false, fmt.Errorf("listing the objects of the bucket %q: %w", bucket, err)
	}

	if len(page.Contents) == 0 {
		return false, nil
	}

	objects := make([]types.ObjectIdentifier, 0, len(page.Contents))
	for _, o := range page.Contents {
		objects = append(objects, types.ObjectIdentifier{Key: o.Key})
	}
	return aws.ToBool(page.IsTruncated), deleteObjects(ctx, c, bucket, objects)
}

// deleteVersionPage deletes the first page of object versions and delete
// markers of bucket, and reports whether more remain.
func deleteVersionPage(ctx context.Context, c S3, bucket string) (bool, error) {
	page, err := c.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
		Bucket:  aws.String(bucket),
		MaxKeys: aws.Int32(deleteBatch),
	})
	if err != nil {
		return false, fmt.Errorf("listing the object versions of the bucket %q: %w", bucket, err)
	}

	objects := make([]types.ObjectIdentifier, 0, len(page.Versions)+len(page.DeleteMarkers))
	for _, v := range page.Versions {
		objects = append(objects, types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
	}
	for _, m := range page.DeleteMarkers {
		objects = append(objects, types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
	}

	if len(objects) == 0 {
		return false, nil
	}
	return aws.ToBool(page.IsTruncated), deleteObjects(ctx, c, bucket, objects)
}

// deleteObjects deletes objects of bucket in one DeleteObjects call.
func deleteObjects(ctx context.Context, c S3, bucket string, objects []types.ObjectIdentifier) error {
	out, err := c.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return fmt.Errorf("deleting the objects of the bucket %q: %w", bucket, err)
	}

	if len(out.Errors) > 0 {
		failed := make([]string, 0, len(out.Errors))
		for _, e := range out.Errors {
			failed = append(failed, aws.ToString(e.Key)+": "+aws.ToString(e.Code))
		}
		return fmt.Errorf("deleting the objects of the bucket %q: %s", bucket, strings.Join(failed, ", "))
	}
	return nil
}
