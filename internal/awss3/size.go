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
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

// sizeWindow is how far back bucketSize looks for a data point. S3 sends
// BucketSizeBytes to CloudWatch once a day.
const sizeWindow = 72 * time.Hour

// bucketSize returns the latest BucketSizeBytes of bucket in the three days
// before now, and false when CloudWatch refuses the read or has no data
// point. Any other error of CloudWatch is returned.
func bucketSize(ctx context.Context, c awskit.CloudWatch, bucket string, now time.Time) (int64, bool, error) {
	out, err := c.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/S3"),
		MetricName: aws.String("BucketSizeBytes"),
		Dimensions: []types.Dimension{
			{Name: aws.String("BucketName"), Value: aws.String(bucket)},
			{Name: aws.String("StorageType"), Value: aws.String("StandardStorage")},
		},
		StartTime:  aws.Time(now.Add(-sizeWindow)),
		EndTime:    aws.Time(now),
		Period:     aws.Int32(int32((24 * time.Hour).Seconds())),
		Statistics: []types.Statistic{types.StatisticAverage},
	})
	switch {
	case awskit.IsAccessDenied(err):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("reading the size of the bucket %s from CloudWatch: %w", bucket, err)
	}

	var latest *types.Datapoint
	for i, p := range out.Datapoints {
		if latest == nil || aws.ToTime(p.Timestamp).After(aws.ToTime(latest.Timestamp)) {
			latest = &out.Datapoints[i]
		}
	}
	if latest == nil {
		return 0, false, nil
	}
	return int64(aws.ToFloat64(latest.Average)), true, nil
}
