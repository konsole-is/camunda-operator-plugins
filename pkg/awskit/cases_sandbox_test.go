//go:build sandbox

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

// The tests of this file run the shared cases against the AWS sandbox
// account, with the credentials of the environment. They need RUN_ID,
// AWS_ACCOUNT_ID, AWS_REGION, and AWS_KMS_REPLICA_REGION.

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

func sandboxAccount(t *testing.T) account {
	s := newSandbox(t)
	replica := os.Getenv("AWS_KMS_REPLICA_REGION")
	require.NotEmpty(t, replica, "AWS_KMS_REPLICA_REGION is not set")
	client := s3.NewFromConfig(s.cfg)

	return account{
		id:       s.account,
		region:   s.cfg.Region,
		replica:  replica,
		prefix:   s.prefix,
		tags:     map[string]string(s.extra),
		boundary: s.boundary,
		settle:   5 * time.Minute,
		iam:      s.iam,
		sts:      sts.NewFromConfig(s.cfg),
		kmsIn:    awskit.KMSIn(s.cfg),
		s3:       client,
		putObjects: func(t *testing.T, bucket string, keys ...string) {
			putSandboxObjects(t, client, bucket, keys)
		},
		setVersioning: func(t *testing.T, bucket string, status s3types.BucketVersioningStatus) {
			setSandboxVersioning(t, client, bucket, status)
		},
	}
}

func TestSTSCasesInTheSandbox(t *testing.T) { runCases(t, sandboxAccount, stsCases) }

func TestIAMCasesInTheSandbox(t *testing.T) { runCases(t, sandboxAccount, iamCases) }

func TestKMSCasesInTheSandbox(t *testing.T) { runCases(t, sandboxAccount, kmsCases) }

func TestS3BucketCasesInTheSandbox(t *testing.T) { runCases(t, sandboxAccount, s3BucketCases) }

func TestS3ObjectCasesInTheSandbox(t *testing.T) { runCases(t, sandboxAccount, s3ObjectCases) }

// putSandboxObjects puts an empty object for each key into bucket.
func putSandboxObjects(t *testing.T, client *s3.Client, bucket string, keys []string) {
	t.Helper()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	slots := make(chan struct{}, 32)

	for _, key := range keys {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(key),
				Body:   strings.NewReader(""),
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	require.NoError(t, errors.Join(errs...))
}

// setSandboxVersioning sets the versioning of bucket to status, and returns
// when S3 applies status to the writes that follow.
func setSandboxVersioning(
	t *testing.T,
	client *s3.Client,
	bucket string,
	status s3types.BucketVersioningStatus,
) {
	t.Helper()
	ctx := context.Background()
	_, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucket),
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: status},
	})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		out, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
		require.NoError(c, err)
		assert.Equal(c, status, out.Status)
	}, 5*time.Minute, time.Second)
	// S3 can apply writes by the old versioning for a while after the change.
	time.Sleep(2 * time.Minute)
}
