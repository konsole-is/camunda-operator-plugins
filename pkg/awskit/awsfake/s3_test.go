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

package awsfake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// createBucket creates the bucket name in the region of c.
func createBucket(c awskit.S3, name, region string) error {
	in := &s3.CreateBucketInput{Bucket: aws.String(name)}
	if region != usEast1 {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}
	_, err := c.CreateBucket(context.Background(), in)
	return err
}

func TestS3GivesANewBucketTheDefaultsOfAWS(t *testing.T) {
	f := NewS3()
	_, err := f.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket: aws.String("bucket-1"),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{
			LocationConstraint: "eu-west-1",
			Tags:               []types.Tag{{Key: aws.String("a"), Value: aws.String("b")}},
		},
	})
	require.NoError(t, err)

	b, ok := f.Bucket("bucket-1")
	require.True(t, ok)
	assert.Equal(t, "eu-west-1", b.Region)
	assert.Equal(t, map[string]string{"a": "b"}, b.Tags)
	assert.Equal(t, DefaultAccount, b.Account)
	assert.True(
		t,
		aws.ToBool(b.PublicAccessBlock.BlockPublicAcls) && aws.ToBool(b.PublicAccessBlock.IgnorePublicAcls) &&
			aws.ToBool(b.PublicAccessBlock.BlockPublicPolicy) && aws.ToBool(b.PublicAccessBlock.RestrictPublicBuckets),
	)
	assert.Equal(t, types.ServerSideEncryptionAes256, b.Encryption.ApplyServerSideEncryptionByDefault.SSEAlgorithm)
	_, err = f.GetBucketLifecycleConfiguration(
		context.Background(),
		&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String("bucket-1")},
	)
	assert.True(t, awskit.IsNotConfigured(err))
}

func TestS3AnswersAnOwnedNameWithBucketAlreadyOwnedByYou(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))

	err := createBucket(f.In("eu-central-1"), "bucket-1", "eu-central-1")

	var owned *types.BucketAlreadyOwnedByYou
	assert.ErrorAs(t, err, &owned, "a bucket name is global across regions")
}

func TestS3RefusesANameOfAnotherAccount(t *testing.T) {
	f := NewS3()
	f.AddForeignBucket("bucket-1", "eu-west-1")

	err := createBucket(f, "bucket-1", "eu-west-1")

	var taken *types.BucketAlreadyExists
	assert.ErrorAs(t, err, &taken)
	assert.Len(t, f.Buckets(), 1)
}

func TestS3NeedsTheLocationConstraintOfItsRegion(t *testing.T) {
	f := NewS3()

	_, err := f.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("bucket-1")})
	assert.Equal(t, "IllegalLocationConstraintException", awskit.ErrorCode(err), "no constraint")

	err = createBucket(f, "bucket-1", "eu-central-1")
	assert.Equal(t, "IllegalLocationConstraintException", awskit.ErrorCode(err), "the constraint of another region")

	assert.Empty(t, f.Buckets())
}

func TestS3RefusesALocationConstraintInUSEast1(t *testing.T) {
	f := NewS3()
	east := f.In(usEast1)

	_, err := east.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket:                    aws.String("bucket-1"),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: usEast1},
	})
	assert.Equal(t, "InvalidLocationConstraint", awskit.ErrorCode(err))

	require.NoError(t, createBucket(east, "bucket-1", usEast1))
	b, _ := f.Bucket("bucket-1")
	assert.Equal(t, usEast1, b.Region)
}

func TestS3RedirectsACallOfAnotherRegion(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	other := f.In("eu-central-1")

	_, err := other.GetBucketTagging(context.Background(), &s3.GetBucketTaggingInput{Bucket: aws.String("bucket-1")})
	assert.Equal(t, "PermanentRedirect", awskit.ErrorCode(err))

	_, err = other.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String("bucket-1")})
	assert.Equal(t, "MovedPermanently", awskit.ErrorCode(err))
}

func TestS3HeadBucketOfAMissingAndOfAForeignBucket(t *testing.T) {
	f := NewS3()
	f.AddForeignBucket("foreign", "eu-west-1")
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))

	_, err := f.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String("missing")})
	var notFound *types.NotFound
	assert.ErrorAs(t, err, &notFound)

	_, err = f.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String("foreign")})
	assert.True(t, awskit.IsAccessDenied(err))
	assert.Equal(t, "Forbidden", awskit.ErrorCode(err))

	out, err := f.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", aws.ToString(out.BucketRegion))
}

func TestS3AnswersACallOnAMissingOrAForeignBucket(t *testing.T) {
	f := NewS3()
	f.AddForeignBucket("foreign", "eu-west-1")

	_, err := f.GetBucketTagging(context.Background(), &s3.GetBucketTaggingInput{Bucket: aws.String("missing")})
	assert.Equal(t, "NoSuchBucket", awskit.ErrorCode(err))
	assert.True(t, awskit.IsNotFound(err))

	_, err = f.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String("missing")})
	var noSuchBucket *types.NoSuchBucket
	assert.ErrorAs(t, err, &noSuchBucket)

	_, err = f.GetBucketTagging(context.Background(), &s3.GetBucketTaggingInput{Bucket: aws.String("foreign")})
	assert.Equal(t, "AccessDenied", awskit.ErrorCode(err))
}

func TestS3AnswersEachMissingConfiguration(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.Edit("bucket-1", func(b *Bucket) {
		b.PublicAccessBlock = nil
		b.Encryption = nil
	})
	bucket := aws.String("bucket-1")
	ctx := context.Background()

	_, err := f.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: bucket})
	assert.Equal(t, "NoSuchTagSet", awskit.ErrorCode(err))
	_, err = f.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: bucket})
	assert.Equal(t, "NoSuchPublicAccessBlockConfiguration", awskit.ErrorCode(err))
	_, err = f.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: bucket})
	assert.Equal(t, "ServerSideEncryptionConfigurationNotFoundError", awskit.ErrorCode(err))
	_, err = f.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: bucket})
	assert.Equal(t, "NoSuchLifecycleConfiguration", awskit.ErrorCode(err))
}

func TestS3ReadsTheConfigurationThatACallWrote(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	bucket := aws.String("bucket-1")
	ctx := context.Background()
	block := &types.PublicAccessBlockConfiguration{BlockPublicAcls: aws.Bool(true), BlockPublicPolicy: aws.Bool(false)}
	rule := types.ServerSideEncryptionRule{
		ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
			SSEAlgorithm:   types.ServerSideEncryptionAwsKms,
			KMSMasterKeyID: aws.String("arn:aws:kms:eu-west-1:111122223333:key/k1"),
		},
		BucketKeyEnabled: aws.Bool(true),
	}
	rules := []types.LifecycleRule{{ID: aws.String("r1"), Status: types.ExpirationStatusEnabled}}

	_, err := f.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket:                         bucket,
		PublicAccessBlockConfiguration: block,
	})
	require.NoError(t, err)
	_, err = f.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: bucket,
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{rule},
		},
	})
	require.NoError(t, err)
	_, err = f.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 bucket,
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: rules},
	})
	require.NoError(t, err)

	gotBlock, err := f.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: bucket})
	require.NoError(t, err)
	assert.Equal(t, block, gotBlock.PublicAccessBlockConfiguration)
	gotEncryption, err := f.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: bucket})
	require.NoError(t, err)
	assert.Equal(t, []types.ServerSideEncryptionRule{rule}, gotEncryption.ServerSideEncryptionConfiguration.Rules)
	gotRules, err := f.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: bucket})
	require.NoError(t, err)
	assert.Equal(t, rules, gotRules.Rules)
}

func TestS3ReplacesTheLifecycleRules(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	put := func(ids ...string) {
		rules := make([]types.LifecycleRule, 0, len(ids))
		for _, id := range ids {
			rules = append(rules, types.LifecycleRule{ID: aws.String(id), Status: types.ExpirationStatusEnabled})
		}
		_, err := f.PutBucketLifecycleConfiguration(context.Background(), &s3.PutBucketLifecycleConfigurationInput{
			Bucket:                 aws.String("bucket-1"),
			LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: rules},
		})
		require.NoError(t, err)
	}

	put("r1", "r2")
	put("r3")

	b, _ := f.Bucket("bucket-1")
	require.Len(t, b.LifecycleRules, 1)
	assert.Equal(t, "r3", aws.ToString(b.LifecycleRules[0].ID))
}

func TestS3ReplacesTheTagSet(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	ctx := context.Background()

	_, err := f.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket:  aws.String("bucket-1"),
		Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("a"), Value: aws.String("1")}}},
	})
	require.NoError(t, err)
	_, err = f.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket:  aws.String("bucket-1"),
		Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("b"), Value: aws.String("2")}}},
	})
	require.NoError(t, err)

	out, err := f.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	assert.Equal(t, []types.Tag{{Key: aws.String("b"), Value: aws.String("2")}}, out.TagSet)

	_, err = f.DeleteBucketTagging(ctx, &s3.DeleteBucketTaggingInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	_, err = f.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String("bucket-1")})
	assert.True(t, awskit.IsNotConfigured(err))
}

func tag(key, value string) types.Tag {
	return types.Tag{Key: aws.String(key), Value: aws.String(value)}
}

func TestS3RefusesMoreThan50Tags(t *testing.T) {
	f := NewS3()
	tags := make([]types.Tag, 51)
	for i := range tags {
		tags[i] = tag(fmt.Sprintf("k%d", i), "v")
	}

	_, err := f.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket:                    aws.String("bucket-1"),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: "eu-west-1", Tags: tags},
	})
	assert.Equal(t, "InvalidTag", awskit.ErrorCode(err), "at create")
	assert.Empty(t, f.Buckets())

	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	_, err = f.PutBucketTagging(context.Background(), &s3.PutBucketTaggingInput{
		Bucket:  aws.String("bucket-1"),
		Tagging: &types.Tagging{TagSet: tags},
	})
	assert.Equal(t, "InvalidTag", awskit.ErrorCode(err), "in a put")
	b, _ := f.Bucket("bucket-1")
	assert.Empty(t, b.Tags)
}

func TestS3RefusesTwoTagsWithOneKey(t *testing.T) {
	f := NewS3()
	tags := []types.Tag{tag("a", "1"), tag("a", "2")}

	_, err := f.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket:                    aws.String("bucket-1"),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: "eu-west-1", Tags: tags},
	})
	assert.Equal(t, "InvalidTag", awskit.ErrorCode(err), "at create")

	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	_, err = f.PutBucketTagging(context.Background(), &s3.PutBucketTaggingInput{
		Bucket:  aws.String("bucket-1"),
		Tagging: &types.Tagging{TagSet: tags},
	})
	assert.Equal(t, "InvalidTag", awskit.ErrorCode(err), "in a put")
	b, _ := f.Bucket("bucket-1")
	assert.Empty(t, b.Tags)
}

func TestS3RefusesAnInvalidBucketName(t *testing.T) {
	f := NewS3()
	for _, name := range []string{
		"ab",
		strings.Repeat("a", 64),
		"Upper",
		"under_score",
		"-start",
		"end-",
	} {
		assert.Equal(t, "InvalidBucketName", awskit.ErrorCode(createBucket(f, name, "eu-west-1")), name)
	}
	assert.Empty(t, f.Buckets())

	for _, name := range []string{"abc", strings.Repeat("a", 63), "a.b-c1"} {
		assert.NoError(t, createBucket(f, name, "eu-west-1"), name)
	}
}

func TestS3AnswersAnOwnedNameInUSEast1WithSuccessAndNoChange(t *testing.T) {
	f := NewS3()
	east := f.In(usEast1)
	create := func(tags ...types.Tag) error {
		_, err := east.CreateBucket(context.Background(), &s3.CreateBucketInput{
			Bucket:                    aws.String("bucket-1"),
			CreateBucketConfiguration: &types.CreateBucketConfiguration{Tags: tags},
		})
		return err
	}
	require.NoError(t, create(tag("a", "1")))

	require.NoError(t, create(tag("b", "2")))

	b, _ := f.Bucket("bucket-1")
	assert.Equal(t, map[string]string{"a": "1"}, b.Tags, "the tags of the second call are not applied")
}

func TestS3RefusesTwoLifecycleRulesWithOneID(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	rule := types.LifecycleRule{ID: aws.String("r1"), Status: types.ExpirationStatusEnabled}

	_, err := f.PutBucketLifecycleConfiguration(context.Background(), &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 aws.String("bucket-1"),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{rule, rule}},
	})

	assert.Equal(t, "InvalidArgument", awskit.ErrorCode(err))
	b, _ := f.Bucket("bucket-1")
	assert.Empty(t, b.LifecycleRules)
}

func TestS3RefusesALifecycleRuleWithoutStatus(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))

	_, err := f.PutBucketLifecycleConfiguration(context.Background(), &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String("bucket-1"),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{
			Rules: []types.LifecycleRule{{ID: aws.String("r1")}},
		},
	})

	var invalid smithy.InvalidParamsError
	assert.ErrorAs(t, err, &invalid, "the SDK refuses the call before it reaches S3")
	b, _ := f.Bucket("bucket-1")
	assert.Empty(t, b.LifecycleRules)
}

func TestS3ListsObjectsInPages(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	keys := make([]string, 2500)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%04d", len(keys)-1-i)
	}
	f.PutObjects("bucket-1", keys...)

	var listed []string
	var pages []int
	var token *string
	for {
		out, err := f.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
			Bucket:            aws.String("bucket-1"),
			ContinuationToken: token,
		})
		require.NoError(t, err)
		pages = append(pages, len(out.Contents))
		for _, o := range out.Contents {
			listed = append(listed, aws.ToString(o.Key))
		}
		if !aws.ToBool(out.IsTruncated) {
			assert.Nil(t, out.NextContinuationToken)
			break
		}
		token = out.NextContinuationToken
	}

	assert.Equal(t, []int{1000, 1000, 500}, pages, "1,000 keys is the default and the most")
	assert.Len(t, listed, 2500)
	assert.IsIncreasing(t, listed, "keys come in order")

	out, err := f.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket:  aws.String("bucket-1"),
		MaxKeys: aws.Int32(5000),
	})
	require.NoError(t, err)
	assert.Len(t, out.Contents, 1000)
}

func TestS3ListsNoObjectWithMaxKeysZero(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.PutObjects("bucket-1", "k1", "k2")

	out, err := f.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket:  aws.String("bucket-1"),
		MaxKeys: aws.Int32(0),
	})

	require.NoError(t, err)
	assert.Empty(t, out.Contents)
	assert.True(t, aws.ToBool(out.IsTruncated))
}

func TestS3RefusesANegativeMaxKeys(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.PutObjects("bucket-1", "k1")

	_, err := f.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket:  aws.String("bucket-1"),
		MaxKeys: aws.Int32(-1),
	})

	assert.Equal(t, "InvalidArgument", awskit.ErrorCode(err))
}

func TestS3RefusesMoreThan1000KeysInOneDelete(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	objects := make([]types.ObjectIdentifier, 1001)
	for i := range objects {
		objects[i] = types.ObjectIdentifier{Key: aws.String(fmt.Sprintf("k%d", i))}
	}

	_, err := f.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
		Bucket: aws.String("bucket-1"),
		Delete: &types.Delete{Objects: objects},
	})

	assert.Equal(t, "MalformedXML", awskit.ErrorCode(err))
}

func TestS3ReportsARefusedKey(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.PutObjects("bucket-1", "k1", "k2")
	f.RefuseDelete("bucket-1", "k1", "AccessDenied")

	out, err := f.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
		Bucket: aws.String("bucket-1"),
		Delete: &types.Delete{
			Objects: []types.ObjectIdentifier{{Key: aws.String("k1")}, {Key: aws.String("k2")}},
			Quiet:   aws.Bool(true),
		},
	})

	require.NoError(t, err)
	require.Len(t, out.Errors, 1)
	assert.Equal(t, "k1", aws.ToString(out.Errors[0].Key))
	assert.Equal(t, "AccessDenied", aws.ToString(out.Errors[0].Code))
	assert.Empty(t, out.Deleted, "a quiet delete names only the errors")
	b, _ := f.Bucket("bucket-1")
	assert.Equal(t, []string{"k1"}, b.Objects)
}

func TestS3RefusesToDeleteABucketWithObjects(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.PutObjects("bucket-1", "k1")

	_, err := f.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String("bucket-1")})
	assert.Equal(t, "BucketNotEmpty", awskit.ErrorCode(err))

	f.Edit("bucket-1", func(b *Bucket) { b.Objects = nil })
	_, err = f.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	_, ok := f.Bucket("bucket-1")
	assert.False(t, ok)
}

func TestS3ListsTheBucketsOfItsAccountInPages(t *testing.T) {
	f := NewS3()
	f.AddForeignBucket("foreign", "eu-west-1")
	require.NoError(t, createBucket(f, "bucket-2", "eu-west-1"))
	require.NoError(t, createBucket(f.In(usEast1), "bucket-1", usEast1))
	require.NoError(t, createBucket(f, "bucket-3", "eu-west-1"))

	var names, regions []string
	var token *string
	for {
		out, err := f.In("eu-central-1").ListBuckets(context.Background(), &s3.ListBucketsInput{
			MaxBuckets:        aws.Int32(2),
			ContinuationToken: token,
		})
		require.NoError(t, err)
		for _, b := range out.Buckets {
			names = append(names, aws.ToString(b.Name))
			regions = append(regions, aws.ToString(b.BucketRegion))
		}
		if out.ContinuationToken == nil {
			break
		}
		token = out.ContinuationToken
	}

	assert.Equal(t, []string{"bucket-1", "bucket-2", "bucket-3"}, names)
	assert.Equal(t, []string{usEast1, "eu-west-1", "eu-west-1"}, regions)
}

func TestS3FailNextFailsTheNextCallOnly(t *testing.T) {
	f := NewS3()
	boom := errors.New("boom")
	f.FailNext("CreateBucket", boom)

	assert.ErrorIs(t, createBucket(f, "bucket-1", "eu-west-1"), boom)
	assert.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	assert.Equal(t, []string{"CreateBucket", "CreateBucket"}, f.Calls())
}

// configure writes a public access block, a KMS encryption rule, and a
// lifecycle rule to the bucket bucket-1 of f, and returns the inputs it sent.
func configure(t *testing.T, f *S3) (
	*types.PublicAccessBlockConfiguration,
	*types.ServerSideEncryptionConfiguration,
	*types.BucketLifecycleConfiguration,
) {
	ctx := context.Background()
	block := &types.PublicAccessBlockConfiguration{BlockPublicAcls: aws.Bool(true)}
	encryption := &types.ServerSideEncryptionConfiguration{Rules: []types.ServerSideEncryptionRule{{
		ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
			SSEAlgorithm:   types.ServerSideEncryptionAwsKms,
			KMSMasterKeyID: aws.String("k1"),
		},
		BucketKeyEnabled: aws.Bool(true),
	}}}
	lifecycle := &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{{
		ID:                             aws.String("r1"),
		Status:                         types.ExpirationStatusEnabled,
		AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(7)},
	}}}

	_, err := f.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket:                         aws.String("bucket-1"),
		PublicAccessBlockConfiguration: block,
	})
	require.NoError(t, err)
	_, err = f.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket:                            aws.String("bucket-1"),
		ServerSideEncryptionConfiguration: encryption,
	})
	require.NoError(t, err)
	_, err = f.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 aws.String("bucket-1"),
		LifecycleConfiguration: lifecycle,
	})
	require.NoError(t, err)
	return block, encryption, lifecycle
}

// assertConfigured asserts that the bucket bucket-1 of f holds what configure wrote.
func assertConfigured(t *testing.T, f *S3) {
	b, _ := f.Bucket("bucket-1")
	assert.True(t, aws.ToBool(b.PublicAccessBlock.BlockPublicAcls))
	assert.Equal(t, "k1", aws.ToString(b.Encryption.ApplyServerSideEncryptionByDefault.KMSMasterKeyID))
	assert.True(t, aws.ToBool(b.Encryption.BucketKeyEnabled))
	assert.Equal(t, "r1", aws.ToString(b.LifecycleRules[0].ID))
	assert.Equal(t, int32(7), aws.ToInt32(b.LifecycleRules[0].AbortIncompleteMultipartUpload.DaysAfterInitiation))
}

func TestS3CopiesDoNotChangeTheStore(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.PutObjects("bucket-1", "k1")
	configure(t, f)
	ctx := context.Background()

	b, _ := f.Bucket("bucket-1")
	b.Tags["x"] = "y"
	b.Objects[0] = "changed"
	*b.PublicAccessBlock.BlockPublicAcls = false
	*b.Encryption.ApplyServerSideEncryptionByDefault.KMSMasterKeyID = "k2"
	*b.Encryption.BucketKeyEnabled = false
	*b.LifecycleRules[0].ID = "r2"
	*b.LifecycleRules[0].AbortIncompleteMultipartUpload.DaysAfterInitiation = 1

	block, err := f.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	*block.PublicAccessBlockConfiguration.BlockPublicAcls = false
	encryption, err := f.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	*encryption.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.KMSMasterKeyID = "k2"
	lifecycle, err := f.GetBucketLifecycleConfiguration(
		ctx,
		&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String("bucket-1")},
	)
	require.NoError(t, err)
	*lifecycle.Rules[0].AbortIncompleteMultipartUpload.DaysAfterInitiation = 1

	again, _ := f.Bucket("bucket-1")
	assert.Empty(t, again.Tags)
	assert.Equal(t, []string{"k1"}, again.Objects)
	assertConfigured(t, f)
}

func TestS3KeepsNoReferenceToTheInputOfAPut(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	block, encryption, lifecycle := configure(t, f)

	*block.BlockPublicAcls = false
	*encryption.Rules[0].ApplyServerSideEncryptionByDefault.KMSMasterKeyID = "k2"
	*encryption.Rules[0].BucketKeyEnabled = false
	*lifecycle.Rules[0].ID = "r2"
	*lifecycle.Rules[0].AbortIncompleteMultipartUpload.DaysAfterInitiation = 1

	assertConfigured(t, f)
}

// versionedBucket returns a fake with the bucket bucket-1 of status, with the
// objects k1 and k2.
func versionedBucket(t *testing.T, status types.BucketVersioningStatus) *S3 {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.PutObjects("bucket-1", "k1", "k2")
	f.Edit("bucket-1", func(b *Bucket) { b.Versioning = status })
	return f
}

func deleteKeys(t *testing.T, f *S3, ids ...types.ObjectIdentifier) *s3.DeleteObjectsOutput {
	out, err := f.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
		Bucket: aws.String("bucket-1"),
		Delete: &types.Delete{Objects: ids},
	})
	require.NoError(t, err)
	return out
}

func TestS3GivesTheVersioningOfABucket(t *testing.T) {
	f := versionedBucket(t, "")

	out, err := f.GetBucketVersioning(
		context.Background(),
		&s3.GetBucketVersioningInput{Bucket: aws.String("bucket-1")},
	)
	require.NoError(t, err)
	assert.Empty(t, out.Status, "a bucket that never had versioning on has no status")

	f.Edit("bucket-1", func(b *Bucket) { b.Versioning = types.BucketVersioningStatusSuspended })
	out, err = f.GetBucketVersioning(context.Background(), &s3.GetBucketVersioningInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	assert.Equal(t, types.BucketVersioningStatusSuspended, out.Status)

	_, err = f.GetBucketVersioning(context.Background(), &s3.GetBucketVersioningInput{Bucket: aws.String("missing")})
	assert.True(t, awskit.IsNotFound(err))
}

func TestS3KeepsTheVersionAndAddsADeleteMarkerWhenVersioningIsOn(t *testing.T) {
	f := versionedBucket(t, types.BucketVersioningStatusEnabled)

	out := deleteKeys(t, f, types.ObjectIdentifier{Key: aws.String("k1")})

	require.Len(t, out.Deleted, 1)
	assert.True(t, aws.ToBool(out.Deleted[0].DeleteMarker))
	b, _ := f.Bucket("bucket-1")
	assert.Equal(t, []string{"k2"}, b.Objects, "the key is gone from ListObjectsV2")
	require.Len(t, b.Versions, 2)
	assert.Equal(t, ObjectVersion{Key: "k1", VersionID: nullVersion}, b.Versions[0])
	assert.True(t, b.Versions[1].DeleteMarker)
	assert.NotEqual(t, nullVersion, b.Versions[1].VersionID)
}

func TestS3ReplacesTheNullVersionWithADeleteMarkerWhenVersioningIsSuspended(t *testing.T) {
	f := versionedBucket(t, types.BucketVersioningStatusSuspended)

	deleteKeys(t, f, types.ObjectIdentifier{Key: aws.String("k1")})

	b, _ := f.Bucket("bucket-1")
	assert.Equal(t, []string{"k2"}, b.Objects)
	assert.Equal(t, []ObjectVersion{{Key: "k1", VersionID: nullVersion, DeleteMarker: true}}, b.Versions)
}

func TestS3DeletesAVersionForGood(t *testing.T) {
	f := versionedBucket(t, types.BucketVersioningStatusEnabled)
	f.Edit("bucket-1", func(b *Bucket) {
		b.Versions = []ObjectVersion{{Key: "k1", VersionID: "v1"}, {Key: "k3", VersionID: "v2", DeleteMarker: true}}
	})

	deleteKeys(
		t,
		f,
		types.ObjectIdentifier{Key: aws.String("k1"), VersionId: aws.String("v1")},
		types.ObjectIdentifier{Key: aws.String("k3"), VersionId: aws.String("v2")},
		types.ObjectIdentifier{Key: aws.String("k2"), VersionId: aws.String(nullVersion)},
	)

	b, _ := f.Bucket("bucket-1")
	assert.Equal(t, []string{"k1"}, b.Objects)
	assert.Empty(t, b.Versions, "a delete with a version ID adds no delete marker")
}

func TestS3ListsEachVersionAndDeleteMarkerInPages(t *testing.T) {
	f := versionedBucket(t, types.BucketVersioningStatusEnabled)
	f.Edit("bucket-1", func(b *Bucket) {
		b.Versions = []ObjectVersion{
			{Key: "k1", VersionID: "v1"},
			{Key: "k3", VersionID: "v2"},
			{Key: "k3", VersionID: "v3", DeleteMarker: true},
		}
	})

	var versions, markers []string
	in := &s3.ListObjectVersionsInput{Bucket: aws.String("bucket-1"), MaxKeys: aws.Int32(2)}
	for pages := 0; ; pages++ {
		require.Less(t, pages, 3)
		out, err := f.ListObjectVersions(context.Background(), in)
		require.NoError(t, err)
		for _, v := range out.Versions {
			versions = append(versions, aws.ToString(v.Key)+"@"+aws.ToString(v.VersionId))
		}
		for _, m := range out.DeleteMarkers {
			markers = append(markers, aws.ToString(m.Key)+"@"+aws.ToString(m.VersionId))
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}

	// Each key lists its newest version first.
	assert.Equal(t, []string{"k1@null", "k1@v1", "k2@null", "k3@v2"}, versions)
	assert.Equal(t, []string{"k3@v3"}, markers)
}

func TestS3RefusesToDeleteABucketWithVersions(t *testing.T) {
	f := versionedBucket(t, types.BucketVersioningStatusEnabled)
	deleteKeys(t, f, types.ObjectIdentifier{Key: aws.String("k1")}, types.ObjectIdentifier{Key: aws.String("k2")})

	_, err := f.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String("bucket-1")})

	assert.Equal(t, "BucketNotEmpty", awskit.ErrorCode(err))
}
