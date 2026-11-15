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

func TestS3GivesANewBucketTheAccountOfTheFake(t *testing.T) {
	f := NewS3()

	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))

	b, ok := f.Bucket("bucket-1")
	require.True(t, ok)
	assert.Equal(t, DefaultAccount, b.Account)
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

	err := createBucket(f, "bucket-1", "eu-central-1")

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

func TestS3HeadBucketOfAForeignBucket(t *testing.T) {
	f := NewS3()
	f.AddForeignBucket("foreign", "eu-west-1")

	_, err := f.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String("foreign")})

	assert.True(t, awskit.IsAccessDenied(err))
	assert.Equal(t, "Forbidden", awskit.ErrorCode(err))
}

func TestS3AnswersACallOnAForeignBucket(t *testing.T) {
	f := NewS3()
	f.AddForeignBucket("foreign", "eu-west-1")

	_, err := f.GetBucketTagging(context.Background(), &s3.GetBucketTaggingInput{Bucket: aws.String("foreign")})

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

	_, err := f.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: bucket})
	assert.Equal(t, "NoSuchPublicAccessBlockConfiguration", awskit.ErrorCode(err))
	_, err = f.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: bucket})
	assert.Equal(t, "ServerSideEncryptionConfigurationNotFoundError", awskit.ErrorCode(err))
}

func TestS3ReadsThePublicAccessBlockThatACallWrote(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	bucket := aws.String("bucket-1")
	ctx := context.Background()
	block := &types.PublicAccessBlockConfiguration{BlockPublicAcls: aws.Bool(true), BlockPublicPolicy: aws.Bool(false)}

	_, err := f.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket:                         bucket,
		PublicAccessBlockConfiguration: block,
	})
	require.NoError(t, err)

	got, err := f.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: bucket})
	require.NoError(t, err)
	assert.Equal(t, block, got.PublicAccessBlockConfiguration)
}

func tag(key, value string) types.Tag {
	return types.Tag{Key: aws.String(key), Value: aws.String(value)}
}

func TestS3RefusesAnInvalidBucketName(t *testing.T) {
	f := NewS3()
	for _, name := range []string{"ab", "-start"} {
		assert.Equal(t, "InvalidBucketName", awskit.ErrorCode(createBucket(f, name, "eu-west-1")), name)
	}
	assert.Empty(t, f.Buckets())

	assert.NoError(t, createBucket(f, "a.b-c1", "eu-west-1"), "a name with a dot")
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

func TestS3FailCreateFailsEachCreateOfTheNameUntilCleared(t *testing.T) {
	f := NewS3()
	denied := &smithy.GenericAPIError{Code: "AccessDenied"}
	f.FailCreate("bucket-1", denied)

	require.ErrorIs(t, createBucket(f, "bucket-1", "eu-west-1"), denied)
	require.ErrorIs(t, createBucket(f, "bucket-1", "eu-west-1"), denied)
	require.NoError(t, createBucket(f, "bucket-2", "eu-west-1"))

	f.FailCreate("bucket-1", nil)
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
}

func TestS3ShowLateShowsTheSettingBeforeAPutForTheGivenReads(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.ShowLate("GetBucketLifecycleConfiguration", 2)
	_, _, lifecycle := configure(t, f)
	get := func() (*s3.GetBucketLifecycleConfigurationOutput, error) {
		return f.GetBucketLifecycleConfiguration(
			context.Background(),
			&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String("bucket-1")},
		)
	}

	for range 2 {
		_, err := get()
		assert.Equal(t, "NoSuchLifecycleConfiguration", awskit.ErrorCode(err))
	}
	out, err := get()
	require.NoError(t, err)
	assert.Equal(t, lifecycle.Rules, out.Rules)
	assertConfigured(t, f)
}

func TestS3ShowLateShowsTheOldSettingUntilReleased(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	f.ShowLate("GetBucketEncryption", -1)
	_, encryption, _ := configure(t, f)
	get := func() types.ServerSideEncryptionByDefault {
		t.Helper()
		out, err := f.GetBucketEncryption(
			context.Background(),
			&s3.GetBucketEncryptionInput{Bucket: aws.String("bucket-1")},
		)
		require.NoError(t, err)
		return *out.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault
	}

	for range 3 {
		assert.Equal(t, types.ServerSideEncryptionAes256, get().SSEAlgorithm, "the SSE-S3 of a new bucket")
	}
	f.ShowLate("GetBucketEncryption", 0)
	assert.Equal(t, *encryption.Rules[0].ApplyServerSideEncryptionByDefault, get())
	_, err := f.GetPublicAccessBlock(
		context.Background(),
		&s3.GetPublicAccessBlockInput{Bucket: aws.String("bucket-1")},
	)
	require.NoError(t, err)
}

func TestS3ShowLateShowsTheOldTagsAfterAPut(t *testing.T) {
	f := NewS3()
	require.NoError(t, createBucket(f, "bucket-1", "eu-west-1"))
	ctx := context.Background()
	put := func(value string) {
		t.Helper()
		_, err := f.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
			Bucket:  aws.String("bucket-1"),
			Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("team"), Value: aws.String(value)}}},
		})
		require.NoError(t, err)
	}
	put("a")
	f.ShowLate("GetBucketTagging", 1)

	put("b")

	out, err := f.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	assert.Equal(t, "a", aws.ToString(out.TagSet[0].Value))
	out, err = f.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String("bucket-1")})
	require.NoError(t, err)
	assert.Equal(t, "b", aws.ToString(out.TagSet[0].Value))
}

func TestS3ShowLateShowsNoTagsOfANewBucket(t *testing.T) {
	f := NewS3()
	f.ShowLate("GetBucketTagging", 1)
	_, err := f.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket: aws.String("bucket-1"),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{
			LocationConstraint: "eu-west-1",
			Tags:               []types.Tag{{Key: aws.String("team"), Value: aws.String("a")}},
		},
	})
	require.NoError(t, err)
	get := func() (*s3.GetBucketTaggingOutput, error) {
		return f.GetBucketTagging(context.Background(), &s3.GetBucketTaggingInput{Bucket: aws.String("bucket-1")})
	}

	_, err = get()
	assert.Equal(t, "NoSuchTagSet", awskit.ErrorCode(err))
	out, err := get()
	require.NoError(t, err)
	assert.Equal(t, "a", aws.ToString(out.TagSet[0].Value))
}
