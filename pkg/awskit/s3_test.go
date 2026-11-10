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
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

// newS3 returns a fake S3 with the empty bucket b1 in its home region.
func newS3(t *testing.T) *awsfake.S3 {
	f := awsfake.NewS3()
	_, err := f.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket:                    aws.String("b1"),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: "eu-west-1"},
	})
	require.NoError(t, err)
	return f
}

// count returns how often operation is in calls.
func count(calls []string, operation string) int {
	return len(slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return c != operation }))
}

func TestEmptyBucketDeletesInBatchesOf1000(t *testing.T) {
	f := newS3(t)
	keys := make([]string, 2500)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%04d", i)
	}
	f.PutObjects("b1", keys...)

	require.NoError(t, awskit.EmptyBucket(context.Background(), f, "b1"))

	b, _ := f.Bucket("b1")
	assert.Empty(t, b.Objects)
	assert.Equal(t, 3, count(f.Calls(), "DeleteObjects"))
}

func TestEmptyBucketNamesARefusedKey(t *testing.T) {
	f := newS3(t)
	f.PutObjects("b1", "k1", "k2")
	f.RefuseDelete("b1", "k1", "AccessDenied")

	err := awskit.EmptyBucket(context.Background(), f, "b1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "k1: AccessDenied")
	b, _ := f.Bucket("b1")
	assert.Equal(t, []string{"k1"}, b.Objects)
}

func TestEmptyBucketOfAnEmptyBucketDeletesNothing(t *testing.T) {
	f := newS3(t)

	require.NoError(t, awskit.EmptyBucket(context.Background(), f, "b1"))

	assert.Zero(t, count(f.Calls(), "DeleteObjects"))
}

func TestEmptyBucketReturnsAnErrorOfS3(t *testing.T) {
	f := newS3(t)
	f.PutObjects("b1", "k1")
	boom := errors.New("boom")
	f.FailNext("DeleteObjects", boom)

	err := awskit.EmptyBucket(context.Background(), f, "b1")

	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), `"b1"`)
}
