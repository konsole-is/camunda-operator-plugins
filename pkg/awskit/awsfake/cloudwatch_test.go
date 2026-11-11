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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var day = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

// sizeQuery is the query of BucketSizeBytes of bucket in [start, end).
func sizeQuery(bucket string, start, end time.Time) *cloudwatch.GetMetricStatisticsInput {
	return &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/S3"),
		MetricName: aws.String("BucketSizeBytes"),
		Dimensions: []types.Dimension{
			{Name: aws.String("BucketName"), Value: aws.String(bucket)},
			{Name: aws.String("StorageType"), Value: aws.String("StandardStorage")},
		},
		StartTime:  aws.Time(start),
		EndTime:    aws.Time(end),
		Period:     aws.Int32(86400),
		Statistics: []types.Statistic{types.StatisticAverage},
	}
}

func averages(out *cloudwatch.GetMetricStatisticsOutput) []float64 {
	var values []float64
	for _, p := range out.Datapoints {
		values = append(values, aws.ToFloat64(p.Average))
	}
	return values
}

func TestCloudWatchGivesThePointsInTheWindow(t *testing.T) {
	f := NewCloudWatch()
	f.SetBucketSize("eu-west-1", "bucket-1", 1, day.Add(-24*time.Hour))
	f.SetBucketSize("eu-west-1", "bucket-1", 2, day)
	f.SetBucketSize("eu-west-1", "bucket-1", 3, day.Add(24*time.Hour))
	f.SetBucketSize("eu-west-1", "bucket-2", 4, day)

	out, err := f.In("eu-west-1").GetMetricStatistics(
		context.Background(),
		sizeQuery("bucket-1", day.Add(-time.Hour), day.Add(24*time.Hour)),
	)

	require.NoError(t, err)
	assert.Equal(t, []float64{2}, averages(out), "the end of the window is not in it")
	assert.Equal(t, day, aws.ToTime(out.Datapoints[0].Timestamp))
}

func TestCloudWatchGivesNoPointForAnotherQuery(t *testing.T) {
	f := NewCloudWatch()
	f.SetBucketSize("eu-west-1", "bucket-1", 1, day)
	query := sizeQuery("bucket-1", day.Add(-time.Hour), day.Add(time.Hour))
	query.Dimensions[1].Value = aws.String("StandardIAStorage")

	out, err := f.In("eu-west-1").GetMetricStatistics(context.Background(), query)

	require.NoError(t, err)
	assert.Empty(t, out.Datapoints)
}

func TestCloudWatchKeepsEachRegionApart(t *testing.T) {
	f := NewCloudWatch()
	f.SetBucketSize("eu-central-1", "bucket-1", 1, day)

	out, err := f.In("eu-west-1").GetMetricStatistics(
		context.Background(),
		sizeQuery("bucket-1", day.Add(-time.Hour), day.Add(time.Hour)),
	)

	require.NoError(t, err)
	assert.Empty(t, out.Datapoints)
}

func TestCloudWatchFailNextFailsTheNextCall(t *testing.T) {
	f := NewCloudWatch()
	boom := errors.New("boom")
	f.FailNext("GetMetricStatistics", boom)
	query := sizeQuery("bucket-1", day, day.Add(time.Hour))

	_, err := f.In("eu-west-1").GetMetricStatistics(context.Background(), query)
	require.ErrorIs(t, err, boom)

	_, err = f.In("eu-west-1").GetMetricStatistics(context.Background(), query)
	require.NoError(t, err)
	assert.Equal(t, []string{"GetMetricStatistics", "GetMetricStatistics"}, f.Calls())
}
