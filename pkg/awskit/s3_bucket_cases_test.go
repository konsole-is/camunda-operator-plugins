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
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

var s3BucketCases = []awsCase{
	{"gives a new bucket the defaults of AWS", func(t *testing.T, a account) {
		ctx := context.Background()
		tags := a.tagged(map[string]string{"a": "b"})
		bucket := a.bucket(t, "s3-defaults", tags)

		a.eventually(t, func(c *assert.CollectT) {
			head, err := a.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
			require.NoError(c, err)
			assert.Equal(c, a.region, aws.ToString(head.BucketRegion))

			tagging, err := a.s3.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucket)})
			require.NoError(c, err)
			assert.ElementsMatch(c, s3Tags(tags), tagging.TagSet)

			block, err := a.s3.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(bucket)})
			require.NoError(c, err)
			flags := block.PublicAccessBlockConfiguration
			assert.True(c, aws.ToBool(flags.BlockPublicAcls))
			assert.True(c, aws.ToBool(flags.IgnorePublicAcls))
			assert.True(c, aws.ToBool(flags.BlockPublicPolicy))
			assert.True(c, aws.ToBool(flags.RestrictPublicBuckets))

			encryption, err := a.s3.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)})
			require.NoError(c, err)
			rules := encryption.ServerSideEncryptionConfiguration.Rules
			require.Len(c, rules, 1)
			assert.Equal(
				c,
				s3types.ServerSideEncryptionAes256,
				rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm,
			)

			_, err = a.s3.GetBucketLifecycleConfiguration(
				ctx,
				&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)},
			)
			assert.True(c, awskit.IsNotConfigured(err), "the lifecycle of a new bucket: %v", err)
		})
	}},
	{"answers a second create of an owned name with BucketAlreadyOwnedByYou", func(t *testing.T, a account) {
		bucket := a.bucket(t, "s3-owned", nil)

		err := a.createBucket(bucket, s3Tags(a.tags))

		var owned *s3types.BucketAlreadyOwnedByYou
		assert.ErrorAs(t, err, &owned)
	}},
	{"refuses a create without the location constraint of its region", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := a.name("s3-no-constraint")
		a.removeBucketLater(t, bucket)

		_, err := a.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})

		assert.Equal(t, "IllegalLocationConstraintException", awskit.ErrorCode(err), "CreateBucket: %v", err)
		_, err = a.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
		assert.ErrorAs(t, err, new(*s3types.NotFound))
	}},
	{
		"answers HeadBucket of a missing bucket with NotFound and of its own with the region",
		func(t *testing.T, a account) {
			ctx := context.Background()
			bucket := a.bucket(t, "s3-head", nil)

			_, err := a.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(a.name("s3-head-missing"))})
			assert.ErrorAs(t, err, new(*s3types.NotFound))

			a.eventually(t, func(c *assert.CollectT) {
				out, err := a.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
				require.NoError(c, err)
				assert.Equal(c, a.region, aws.ToString(out.BucketRegion))
			})
		},
	},
	{"answers a call on a missing bucket with NoSuchBucket", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := aws.String(a.name("s3-missing"))

		_, err := a.s3.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: bucket})
		assert.Equal(t, "NoSuchBucket", awskit.ErrorCode(err), "GetBucketTagging: %v", err)
		assert.True(t, awskit.IsNotFound(err))

		_, err = a.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: bucket})
		assert.ErrorAs(t, err, new(*s3types.NoSuchBucket))
	}},
	{"answers a missing tag set and lifecycle", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := aws.String(a.bucket(t, "s3-unset", nil))

		_, err := a.s3.DeleteBucketTagging(ctx, &s3.DeleteBucketTaggingInput{Bucket: bucket})

		require.NoError(t, err)
		a.eventually(t, func(c *assert.CollectT) {
			_, err := a.s3.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: bucket})
			assert.Equal(c, "NoSuchTagSet", awskit.ErrorCode(err), "GetBucketTagging: %v", err)
		})
		_, err = a.s3.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: bucket})
		assert.Equal(t, "NoSuchLifecycleConfiguration", awskit.ErrorCode(err), "the lifecycle: %v", err)
	}},
	{"reads the encryption and the lifecycle that a call wrote", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := aws.String(a.bucket(t, "s3-written", nil))
		key := a.key(t, false)
		rule := s3types.ServerSideEncryptionRule{
			ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{
				SSEAlgorithm:   s3types.ServerSideEncryptionAwsKms,
				KMSMasterKeyID: key.Arn,
			},
			BucketKeyEnabled: aws.Bool(true),
		}
		rules := []s3types.LifecycleRule{abortUploadsRule("r1")}

		_, err := a.s3.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
			Bucket: bucket,
			ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{
				Rules: []s3types.ServerSideEncryptionRule{rule},
			},
		})
		require.NoError(t, err)
		putLifecycle(t, a, bucket, rules)

		a.eventually(t, func(c *assert.CollectT) {
			encryption, err := a.s3.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: bucket})
			require.NoError(c, err)
			got := encryption.ServerSideEncryptionConfiguration.Rules
			require.Len(c, got, 1)
			assert.Equal(c, rule.ApplyServerSideEncryptionByDefault, got[0].ApplyServerSideEncryptionByDefault)
			assert.Equal(c, rule.BucketKeyEnabled, got[0].BucketKeyEnabled)

			assert.Equal(c, lifecycleShapes(rules), readLifecycle(c, a, bucket))
		})
	}},
	{"replaces the lifecycle rules", func(t *testing.T, a account) {
		bucket := aws.String(a.bucket(t, "s3-lifecycle", nil))
		putLifecycle(t, a, bucket, []s3types.LifecycleRule{abortUploadsRule("r1"), abortUploadsRule("r2")})

		putLifecycle(t, a, bucket, []s3types.LifecycleRule{abortUploadsRule("r3")})

		a.eventually(t, func(c *assert.CollectT) {
			want := lifecycleShapes([]s3types.LifecycleRule{abortUploadsRule("r3")})
			assert.Equal(c, want, readLifecycle(c, a, bucket))
		})
	}},
	{"replaces the tag set and deletes it", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := aws.String(a.bucket(t, "s3-tags", nil))
		put := func(tags map[string]string) {
			_, err := a.s3.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
				Bucket:  bucket,
				Tagging: &s3types.Tagging{TagSet: s3Tags(tags)},
			})
			require.NoError(t, err)
		}
		put(a.tagged(map[string]string{"a": "1"}))

		put(a.tagged(map[string]string{"b": "2"}))

		a.eventually(t, func(c *assert.CollectT) {
			out, err := a.s3.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: bucket})
			require.NoError(c, err)
			assert.ElementsMatch(c, s3Tags(a.tagged(map[string]string{"b": "2"})), out.TagSet)
		})
		_, err := a.s3.DeleteBucketTagging(ctx, &s3.DeleteBucketTaggingInput{Bucket: bucket})
		require.NoError(t, err)
		a.eventually(t, func(c *assert.CollectT) {
			_, err := a.s3.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: bucket})
			assert.True(c, awskit.IsNotConfigured(err), "GetBucketTagging after the delete: %v", err)
		})
	}},
	{"refuses more than 50 tags", func(t *testing.T, a account) {
		tags := s3Tags(a.tags)
		for i := 0; len(tags) <= 50; i++ {
			tags = append(tags, s3types.Tag{Key: aws.String(fmt.Sprintf("k%d", i)), Value: aws.String("v")})
		}
		assertRefusedTags(t, a, "s3-51-tags", "s3-51-tags-put", tags)
	}},
	{"refuses two tags with one key", func(t *testing.T, a account) {
		tags := append(
			s3Tags(a.tags),
			s3types.Tag{Key: aws.String("a"), Value: aws.String("1")},
			s3types.Tag{Key: aws.String("a"), Value: aws.String("2")},
		)
		assertRefusedTags(t, a, "s3-dup-tags", "s3-dup-tags-put", tags)
	}},
	{"refuses an invalid bucket name", func(t *testing.T, a account) {
		ofLength := func(n int) string { return a.name("s3-" + strings.Repeat("a", n-len(a.prefix)-len("-s3-"))) }

		for _, name := range []string{a.name("s3-Upper"), a.name("s3-under_score"), a.name("s3-end-"), ofLength(64)} {
			a.removeBucketLater(t, name)
			err := a.createBucket(name, s3Tags(a.tags))
			assert.Equal(t, "InvalidBucketName", awskit.ErrorCode(err), "CreateBucket of %s: %v", name, err)
		}

		name := ofLength(63)
		a.removeBucketLater(t, name)
		assert.NoError(t, a.createBucket(name, s3Tags(a.tags)), "a name of 63 characters")
	}},
	{"refuses two lifecycle rules with one ID", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := aws.String(a.bucket(t, "s3-rule-ids", nil))

		_, err := a.s3.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
			Bucket: bucket,
			LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
				Rules: []s3types.LifecycleRule{abortUploadsRule("r1"), abortUploadsRule("r1")},
			},
		})

		assert.Equal(t, "InvalidArgument", awskit.ErrorCode(err), "PutBucketLifecycleConfiguration: %v", err)
		_, err = a.s3.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: bucket})
		assert.True(t, awskit.IsNotConfigured(err), "the lifecycle: %v", err)
	}},
	{"refuses a lifecycle rule without a status", func(t *testing.T, a account) {
		ctx := context.Background()
		bucket := aws.String(a.bucket(t, "s3-rule-status", nil))
		rule := abortUploadsRule("r1")
		rule.Status = ""

		_, err := a.s3.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
			Bucket:                 bucket,
			LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{Rules: []s3types.LifecycleRule{rule}},
		})

		assert.ErrorAs(t, err, new(smithy.InvalidParamsError), "the SDK refuses the call before it reaches S3")
		_, err = a.s3.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: bucket})
		assert.True(t, awskit.IsNotConfigured(err), "the lifecycle: %v", err)
	}},
	{"lists the buckets of its account in pages", func(t *testing.T, a account) {
		want := make([]string, 0, 3)
		for _, suffix := range []string{"s3-list-a", "s3-list-b", "s3-list-c"} {
			want = append(want, a.bucket(t, suffix, nil))
		}

		// Other runs share the account, so the case reads only its own buckets.
		prefix := a.name("s3-list-")
		a.eventually(t, func(c *assert.CollectT) {
			var names, regions []string
			var token *string
			for {
				out, err := a.s3.ListBuckets(context.Background(), &s3.ListBucketsInput{
					MaxBuckets:        aws.Int32(2),
					ContinuationToken: token,
				})
				require.NoError(c, err)
				for _, b := range out.Buckets {
					if strings.HasPrefix(aws.ToString(b.Name), prefix) {
						names = append(names, aws.ToString(b.Name))
						regions = append(regions, aws.ToString(b.BucketRegion))
					}
				}
				if out.ContinuationToken == nil {
					break
				}
				token = out.ContinuationToken
			}

			assert.Equal(c, want, names)
			assert.Equal(c, []string{a.region, a.region, a.region}, regions)
		})
	}},
}

// abortUploadsRule returns a lifecycle rule id that aborts an incomplete
// upload of each object after 7 days, in the shape that the aws-s3 plugin
// writes. S3 refuses a rule with no action.
func abortUploadsRule(id string) s3types.LifecycleRule {
	return s3types.LifecycleRule{
		ID:                             aws.String(id),
		Status:                         s3types.ExpirationStatusEnabled,
		Filter:                         &s3types.LifecycleRuleFilter{Prefix: aws.String("")},
		AbortIncompleteMultipartUpload: &s3types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(7)},
	}
}

func putLifecycle(t *testing.T, a account, bucket *string, rules []s3types.LifecycleRule) {
	t.Helper()
	_, err := a.s3.PutBucketLifecycleConfiguration(context.Background(), &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 bucket,
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{Rules: rules},
	})
	require.NoError(t, err)
}

// lifecycleShape holds the fields of a lifecycle rule that the aws-s3 plugin
// reads.
type lifecycleShape struct {
	ID, Prefix string
	Status     s3types.ExpirationStatus
	Filtered   bool
	AbortDays  int32
}

func lifecycleShapes(rules []s3types.LifecycleRule) []lifecycleShape {
	shapes := make([]lifecycleShape, 0, len(rules))
	for _, r := range rules {
		shape := lifecycleShape{ID: aws.ToString(r.ID), Status: r.Status, Filtered: r.Filter != nil}
		if r.Filter != nil {
			shape.Prefix = aws.ToString(r.Filter.Prefix)
		}
		if r.AbortIncompleteMultipartUpload != nil {
			shape.AbortDays = aws.ToInt32(r.AbortIncompleteMultipartUpload.DaysAfterInitiation)
		}
		shapes = append(shapes, shape)
	}
	return shapes
}

func readLifecycle(c *assert.CollectT, a account, bucket *string) []lifecycleShape {
	out, err := a.s3.GetBucketLifecycleConfiguration(
		context.Background(),
		&s3.GetBucketLifecycleConfigurationInput{Bucket: bucket},
	)
	require.NoError(c, err)
	return lifecycleShapes(out.Rules)
}

// assertRefusedTags asserts that S3 refuses tags with InvalidTag at the
// create of the bucket a.name(created), which then does not exist, and in a
// put on the bucket a.name(put), whose tags stay those of the account.
func assertRefusedTags(t *testing.T, a account, created, put string, tags []s3types.Tag) {
	ctx := context.Background()
	name := a.name(created)
	a.removeBucketLater(t, name)

	err := a.createBucket(name, tags)

	assert.Equal(t, "InvalidTag", awskit.ErrorCode(err), "CreateBucket: %v", err)
	_, err = a.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(name)})
	assert.True(t, errors.As(err, new(*s3types.NotFound)), "HeadBucket after a refused create: %v", err)

	bucket := aws.String(a.bucket(t, put, nil))
	_, err = a.s3.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket:  bucket,
		Tagging: &s3types.Tagging{TagSet: tags},
	})
	assert.Equal(t, "InvalidTag", awskit.ErrorCode(err), "PutBucketTagging: %v", err)
	a.eventually(t, func(c *assert.CollectT) {
		out, err := a.s3.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: bucket})
		require.NoError(c, err)
		assert.ElementsMatch(c, s3Tags(a.tags), out.TagSet)
	})
}
