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
	"testing"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit/awsfake"
)

func fakeAccount(_ *testing.T) account {
	kms, s3 := awsfake.NewKMS(), awsfake.NewS3()
	return account{
		id:       awsfake.DefaultAccount,
		region:   "eu-west-1",
		replica:  "eu-central-1",
		prefix:   "cpo-ci-fake-awskit",
		tags:     map[string]string{"cpo-ci-run": "cpo-ci-fake"},
		boundary: "arn:aws:iam::" + awsfake.DefaultAccount + ":policy/cpo-sandbox-boundary",
		iam:      awsfake.NewIAM(),
		sts:      awsfake.NewSTS(),
		kmsIn:    kms.In,
		s3:       s3,
		putObjects: func(_ *testing.T, bucket string, keys ...string) {
			s3.PutObjects(bucket, keys...)
		},
		setVersioning: func(_ *testing.T, bucket string, status s3types.BucketVersioningStatus) {
			s3.Edit(bucket, func(b *awsfake.Bucket) { b.Versioning = status })
		},
	}
}

func TestSTSCasesOnTheFake(t *testing.T) { runCases(t, fakeAccount, stsCases) }

func TestIAMCasesOnTheFake(t *testing.T) { runCases(t, fakeAccount, iamCases) }

func TestKMSCasesOnTheFake(t *testing.T) { runCases(t, fakeAccount, kmsCases) }
