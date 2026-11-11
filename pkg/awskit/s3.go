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
	"fmt"
	"strings"

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
	}
}
