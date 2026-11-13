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
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

var _ awskit.CloudWatch = cloudWatchRegion{}

// CloudWatch is an in-memory CloudWatch of every region that holds the
// metric BucketSizeBytes of S3 only.
type CloudWatch struct {
	mu sync.Mutex
	// sizes are the data points of each region/bucket.
	sizes map[string][]types.Datapoint
	fail  failures
}

// NewCloudWatch returns a CloudWatch without data points.
func NewCloudWatch() *CloudWatch {
	return &CloudWatch{sizes: map[string][]types.Datapoint{}}
}

// In returns the CloudWatch of region.
func (f *CloudWatch) In(region string) awskit.CloudWatch {
	return cloudWatchRegion{f: f, region: region}
}

// SetBucketSize adds a data point of AWS/S3 BucketSizeBytes, StorageType
// StandardStorage, for bucket in region at the time at.
func (f *CloudWatch) SetBucketSize(region, bucket string, bytes float64, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := region + "/" + bucket
	f.sizes[key] = append(f.sizes[key], types.Datapoint{
		Average:   aws.Float64(bytes),
		Timestamp: aws.Time(at),
		Unit:      types.StandardUnitBytes,
	})
}

// FailNext makes the next call of operation, in any region, return err.
func (f *CloudWatch) FailNext(operation string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail.next(operation, err)
}

// Calls returns the names of the operations called in every region, in call
// order.
func (f *CloudWatch) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.fail.calls)
}

// cloudWatchRegion is the CloudWatch API of one region of a fake.
type cloudWatchRegion struct {
	f      *CloudWatch
	region string
}

// GetMetricStatistics returns the data points of SetBucketSize in the region
// whose time is in [StartTime, EndTime), for a query of AWS/S3 BucketSizeBytes
// with the dimensions BucketName and StorageType StandardStorage. Any other
// query has no data points.
func (r cloudWatchRegion) GetMetricStatistics(
	_ context.Context,
	in *cloudwatch.GetMetricStatisticsInput,
	_ ...func(*cloudwatch.Options),
) (*cloudwatch.GetMetricStatisticsOutput, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if err := r.f.fail.call("GetMetricStatistics"); err != nil {
		return nil, err
	}

	out := &cloudwatch.GetMetricStatisticsOutput{Label: in.MetricName}
	dimensions := map[string]string{}
	for _, d := range in.Dimensions {
		dimensions[aws.ToString(d.Name)] = aws.ToString(d.Value)
	}
	if aws.ToString(in.Namespace) != "AWS/S3" || aws.ToString(in.MetricName) != "BucketSizeBytes" ||
		len(dimensions) != 2 || dimensions["StorageType"] != "StandardStorage" {
		return out, nil
	}

	start, end := aws.ToTime(in.StartTime), aws.ToTime(in.EndTime)
	for _, p := range r.f.sizes[cmp.Or(r.region, defaultHome)+"/"+dimensions["BucketName"]] {
		if at := aws.ToTime(p.Timestamp); !at.Before(start) && at.Before(end) {
			out.Datapoints = append(out.Datapoints, p)
		}
	}
	return out, nil
}
