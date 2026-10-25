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
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// s3API is the part of the S3 client that bucketClient calls.
type s3API interface {
	HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CreateBucket(
		ctx context.Context,
		in *s3.CreateBucketInput,
		opts ...func(*s3.Options),
	) (*s3.CreateBucketOutput, error)
	ListObjectsV2(
		ctx context.Context,
		in *s3.ListObjectsV2Input,
		opts ...func(*s3.Options),
	) (*s3.ListObjectsV2Output, error)
	DeleteObjects(
		ctx context.Context,
		in *s3.DeleteObjectsInput,
		opts ...func(*s3.Options),
	) (*s3.DeleteObjectsOutput, error)
	DeleteBucket(
		ctx context.Context,
		in *s3.DeleteBucketInput,
		opts ...func(*s3.Options),
	) (*s3.DeleteBucketOutput, error)
}

// deleteBatch is the most keys that one DeleteObjects call takes.
const deleteBatch = 1000

// bucketClient creates and deletes buckets through the S3 API of RustFS.
type bucketClient struct{ api s3API }

// newBucketClient returns a client of the S3 API at endpoint that signs with
// the access key pair.
func newBucketClient(endpoint, region, accessKey, secretKey string) *bucketClient {
	return &bucketClient{api: s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       region,
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		// The SDK sets no read timeout for S3, so a stalled connection holds
		// the reconcile without one.
		HTTPClient: awshttp.NewBuildableClient().WithTimeout(callTimeout),
		// An S3-compatible store need not accept the checksums that the SDK
		// adds by default.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})}
}

// EnsureBucket creates the bucket name when it does not exist, and reports
// whether it did. A bucket of the name that another RustFS user owns is an
// error.
func (c *bucketClient) EnsureBucket(ctx context.Context, name string) (bool, error) {
	exists, err := c.exists(ctx, name)
	if err != nil || exists {
		return false, err
	}

	_, err = c.api.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
	var owned *types.BucketAlreadyOwnedByYou
	var taken *types.BucketAlreadyExists
	switch {
	case errors.As(err, &owned):
		return false, nil
	case errors.As(err, &taken):
		return false, fmt.Errorf("bucket %q exists and belongs to another RustFS user", name)
	case err != nil:
		return false, fmt.Errorf("creating the bucket %q: %w", name, err)
	}
	return true, nil
}

// DeleteBucket deletes every object of the bucket name and then the bucket,
// and reports whether the bucket existed.
func (c *bucketClient) DeleteBucket(ctx context.Context, name string) (bool, error) {
	exists, err := c.exists(ctx, name)
	if err != nil || !exists {
		return false, err
	}

	if err := emptyBucket(ctx, c.api, name); err != nil {
		return false, err
	}

	if _, err := c.api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)}); err != nil {
		return false, fmt.Errorf("deleting the bucket %q: %w", name, err)
	}
	return true, nil
}

func (c *bucketClient) exists(ctx context.Context, name string) (bool, error) {
	_, err := c.api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(name)})
	var notFound *types.NotFound
	switch {
	case errors.As(err, &notFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reading the bucket %q: %w", name, err)
	}
	return true, nil
}

// emptyBucket deletes every object of the bucket name. A key that RustFS
// refuses to delete is an error. Versioning stays off on the buckets of the
// plugin, so no object version is left behind.
func emptyBucket(ctx context.Context, api s3API, name string) error {
	for {
		page, err := api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(name),
			MaxKeys: aws.Int32(deleteBatch),
		})
		if err != nil {
			return fmt.Errorf("listing the objects of the bucket %q: %w", name, err)
		}

		if len(page.Contents) == 0 {
			return nil
		}

		objects := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, o := range page.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: o.Key})
		}
		out, err := api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(name),
			Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("deleting the objects of the bucket %q: %w", name, err)
		}

		if len(out.Errors) > 0 {
			failed := make([]string, 0, len(out.Errors))
			for _, e := range out.Errors {
				failed = append(failed, aws.ToString(e.Key)+": "+aws.ToString(e.Code))
			}
			return fmt.Errorf("deleting the objects of the bucket %q: %s", name, strings.Join(failed, ", "))
		}
	}
}
