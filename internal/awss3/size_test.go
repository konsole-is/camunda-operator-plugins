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

package awss3

import (
	"context"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func TestBucketSizeIsUnknownWithoutAccess(t *testing.T) {
	f := awsfake.NewCloudWatch()
	f.FailNext("GetMetricStatistics", &smithy.GenericAPIError{Code: "AccessDenied"})

	size, known, err := bucketSize(context.Background(), f.In("eu-west-1"), "bucket-1", now)

	require.NoError(t, err)
	assert.False(t, known)
	assert.Zero(t, size)
}

func TestBucketSizeIsUnknownWithoutADataPoint(t *testing.T) {
	f := awsfake.NewCloudWatch()
	f.SetBucketSize("eu-west-1", "bucket-1", 1, now.Add(-4*24*time.Hour))

	_, known, err := bucketSize(context.Background(), f.In("eu-west-1"), "bucket-1", now)

	require.NoError(t, err)
	assert.False(t, known, "a point older than three days does not count")
}

func TestBucketSizeTakesTheLatestDataPoint(t *testing.T) {
	f := awsfake.NewCloudWatch()
	f.SetBucketSize("eu-west-1", "bucket-1", 2048, now.Add(-12*time.Hour))
	f.SetBucketSize("eu-west-1", "bucket-1", 1024, now.Add(-36*time.Hour))

	size, known, err := bucketSize(context.Background(), f.In("eu-west-1"), "bucket-1", now)

	require.NoError(t, err)
	assert.True(t, known)
	assert.Equal(t, int64(2048), size)
}

func TestBucketSizeReturnsAnotherError(t *testing.T) {
	f := awsfake.NewCloudWatch()
	f.FailNext("GetMetricStatistics", &smithy.GenericAPIError{Code: "Throttling"})

	_, _, err := bucketSize(context.Background(), f.In("eu-west-1"), "bucket-1", now)

	assert.Error(t, err)
}
