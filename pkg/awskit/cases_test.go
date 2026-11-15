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

// The cases of the *_cases_test.go files hold what the AWS APIs promise and
// what awskit and the plugins read from them. They run against the fakes of
// awsfake, and with the sandbox build tag against the AWS sandbox account,
// so that a fake that answers otherwise than AWS fails a case.
//
// These tests of awsfake stay fake-only, because the sandbox cannot run them:
//   - EKS: the sandbox has no EKS cluster.
//   - CloudWatch: the boundary allows no CloudWatch call, and a new bucket
//     has no data point for about a day, so a real call shows only the
//     AccessDenied and empty answers that the fake already gives.
//   - S3 in another region, in us-east-1, or of another account: the
//     boundary denies S3 outside the region of the sandbox, and the account
//     owns no bucket of another account.
//   - A KMS deletion window other than 7 days: the boundary refuses it.
//   - A write of a public access block: the boundary refuses it.
//   - An IAM instance profile: the CI role may not create one.
//   - A bucket name that ends in a dash: the SDK sends the call to a host
//     name that does not resolve, so it never reaches S3.
//   - The knobs of a fake, such as FailNext and ShowLate: AWS has none.

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// sandboxTrust is a trust policy that no principal can use.
const sandboxTrust = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny",` +
	`"Principal":{"Service":"pods.eks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// account is where the shared cases run: the fakes of awsfake, or the AWS
// sandbox account.
type account struct {
	id, region, replica string
	// prefix starts each name that a case creates.
	prefix string
	// tags go on each role, key, and bucket that a case creates.
	tags     map[string]string
	boundary string
	// settle is how long a read can still show the state before a write.
	settle time.Duration

	iam   awskit.IAM
	sts   awskit.STS
	kmsIn func(region string) awskit.KMS
	s3    awskit.S3
	// putObjects and setVersioning write what the awskit interfaces cannot.
	putObjects    func(t *testing.T, bucket string, keys ...string)
	setVersioning func(t *testing.T, bucket string, status s3types.BucketVersioningStatus)
}

// awsCase is one promise of an AWS API, checked against an account.
type awsCase struct {
	name string
	run  func(t *testing.T, a account)
}

// runCases runs each case as a parallel subtest, each with its own account.
func runCases(t *testing.T, newAccount func(t *testing.T) account, cases []awsCase) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, newAccount(t))
		})
	}
}

func (a account) name(suffix string) string {
	return a.prefix + "-" + suffix
}

// tagged returns the tags of the account with extra.
func (a account) tagged(extra map[string]string) map[string]string {
	tags := maps.Clone(a.tags)
	maps.Copy(tags, extra)
	return tags
}

// eventually fails the case when check does not pass within the settle time
// of the account, or within a second.
func (a account) eventually(t *testing.T, check func(c *assert.CollectT)) {
	t.Helper()
	require.EventuallyWithT(t, check, max(a.settle, time.Second), 200*time.Millisecond)
}

// role creates the role a.name(suffix) with the boundary and the tags of the
// account, and deletes it at the end of the case.
func (a account) role(t *testing.T, suffix string) string {
	t.Helper()
	name := a.name(suffix)
	t.Cleanup(func() { a.deleteRole(t, name) })

	_, err := a.iam.CreateRole(context.Background(), &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(sandboxTrust),
		PermissionsBoundary:      aws.String(a.boundary),
		Tags:                     iamTags(a.tags),
	})
	require.NoError(t, err)
	return name
}

// deleteRole deletes the role name with its policies, whatever its UID tag,
// and logs a failure.
func (a account) deleteRole(t *testing.T, name string) {
	ctx := context.Background()
	out, err := a.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if awskit.IsNotFound(err) {
		return
	}

	if err == nil {
		uid := ""
		if i := slices.IndexFunc(out.Role.Tags, func(tag iamtypes.Tag) bool {
			return aws.ToString(tag.Key) == awskit.TagRequestUID
		}); i >= 0 {
			uid = aws.ToString(out.Role.Tags[i].Value)
		}
		_, err = awskit.DeleteRole(ctx, a.iam, name, uid)
	}
	if err != nil {
		t.Logf("the sweep of the run removes the role %s: %v", name, err)
	}
}

// key creates a key with the tags of the account in its region, and
// schedules its deletion at the end of the case.
func (a account) key(t *testing.T, multiRegion bool) *kmstypes.KeyMetadata {
	t.Helper()
	var id string
	t.Cleanup(func() {
		if id != "" {
			a.removeKey(t, a.region, id)
		}
	})

	out, err := a.kmsIn(a.region).CreateKey(context.Background(), &kms.CreateKeyInput{
		MultiRegion: aws.Bool(multiRegion),
		Tags:        kmsTags(a.tags),
	})
	require.NoError(t, err)
	id = aws.ToString(out.KeyMetadata.KeyId)
	return out.KeyMetadata
}

// removeKey schedules the deletion of the key id in region in 7 days, and
// logs a failure. A missing key, or one that is pending deletion already, is
// no failure.
func (a account) removeKey(t *testing.T, region, id string) {
	ctx := context.Background()
	c := a.kmsIn(region)
	// KMS refuses to schedule a replica that it is still creating.
	deadline := time.Now().Add(max(a.settle, time.Second))
	for {
		_, err := c.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
			KeyId:               aws.String(id),
			PendingWindowInDays: aws.Int32(7),
		})
		if err == nil || awskit.IsNotFound(err) {
			return
		}

		if awskit.ErrorCode(err) == "KMSInvalidStateException" {
			out, describeErr := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(id)})
			if describeErr == nil && slices.Contains(
				[]kmstypes.KeyState{kmstypes.KeyStatePendingDeletion, kmstypes.KeyStatePendingReplicaDeletion},
				out.KeyMetadata.KeyState,
			) {
				return
			}
			if time.Now().Before(deadline) {
				time.Sleep(time.Second)
				continue
			}
		}
		t.Logf("the sweep of the run removes the key %s in %s: %v", id, region, err)
		return
	}
}

// alias creates the alias alias/<a.name(suffix)> of the key id in the region
// of the account, and deletes it at the end of the case. It returns the alias
// and the error of CreateAlias.
func (a account) alias(t *testing.T, suffix, id string) (string, error) {
	t.Helper()
	name := "alias/" + a.name(suffix)
	c := a.kmsIn(a.region)
	t.Cleanup(func() {
		_, err := c.DeleteAlias(context.Background(), &kms.DeleteAliasInput{AliasName: aws.String(name)})
		if err != nil && !awskit.IsNotFound(err) {
			t.Logf("the sweep of the run removes the alias %s: %v", name, err)
		}
	})

	_, err := c.CreateAlias(context.Background(), &kms.CreateAliasInput{
		AliasName:   aws.String(name),
		TargetKeyId: aws.String(id),
	})
	return name, err
}

// bucket creates the bucket a.name(suffix) in the region of the account,
// with the tags of the account and extra, and removes it at the end of the
// case.
func (a account) bucket(t *testing.T, suffix string, extra map[string]string) string {
	t.Helper()
	name := a.name(suffix)
	a.removeBucketLater(t, name)
	require.NoError(t, a.createBucket(name, s3Tags(a.tagged(extra))))
	return name
}

// createBucket creates the bucket name in the region of the account, with
// tags.
func (a account) createBucket(name string, tags []s3types.Tag) error {
	_, err := a.s3.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket: aws.String(name),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(a.region),
			Tags:               tags,
		},
	})
	return err
}

// removeBucketLater empties and deletes the bucket name at the end of the
// case, and logs a failure. A bucket that does not exist is no failure.
func (a account) removeBucketLater(t *testing.T, name string) {
	t.Cleanup(func() {
		ctx := context.Background()
		_, err := a.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(name)})
		// S3 answers BadRequest for a name that no bucket can have.
		if errors.As(err, new(*s3types.NotFound)) || awskit.ErrorCode(err) == "BadRequest" {
			return
		}

		for remain := true; remain && err == nil; {
			remain, err = awskit.EmptyBucketWithin(ctx, a.s3, name, time.Minute)
		}
		if err == nil {
			_, err = a.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)})
		}
		if err != nil && !awskit.IsNotFound(err) {
			t.Logf("the sweep of the run removes the bucket %s: %v", name, err)
		}
	})
}

func iamTags(tags map[string]string) []iamtypes.Tag {
	out := make([]iamtypes.Tag, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, iamtypes.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

func kmsTags(tags map[string]string) []kmstypes.Tag {
	out := make([]kmstypes.Tag, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, kmstypes.Tag{TagKey: aws.String(k), TagValue: aws.String(tags[k])})
	}
	return out
}

func s3Tags(tags map[string]string) []s3types.Tag {
	out := make([]s3types.Tag, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, s3types.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

func TestEventuallyRetriesUntilTheReadShowsTheWrite(t *testing.T) {
	a := account{settle: time.Second}
	reads := 0

	a.eventually(t, func(c *assert.CollectT) {
		reads++
		assert.GreaterOrEqual(c, reads, 3)
	})

	assert.Equal(t, 3, reads)
}
