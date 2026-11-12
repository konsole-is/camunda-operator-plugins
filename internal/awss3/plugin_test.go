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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
)

const roleARN = "arn:aws:iam::111122223333:role/cpo-test"

// request is the request orders-backup of the claim orders in team-a, with
// the recorded rows rows.
func request(rows ...pluginsv1alpha1.ResourceRef) *pluginsv1alpha1.PluginRequest {
	return &pluginsv1alpha1.PluginRequest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "orders-backup", UID: "uid-1"},
		Spec: pluginsv1alpha1.PluginRequestSpec{
			ClaimRef:   pluginsv1alpha1.ClaimRef{Name: "orders"},
			Target:     pluginsv1alpha1.TargetRef{Kind: "CamundaCluster", Name: "orders"},
			Parameters: &apiextensionsv1.JSON{Raw: []byte(`{}`)},
		},
		Status: pluginsv1alpha1.PluginRequestStatus{Resources: rows},
	}
}

func bucketRow(name, region string) pluginsv1alpha1.ResourceRef {
	return pluginsv1alpha1.ResourceRef{Kind: kindS3Bucket, Name: name, Location: region}
}

func TestBucketOfComputesTheBucketOfTheParameters(t *testing.T) {
	ref := bucketOf(request(), Params{BucketPrefix: "team", Region: "eu-central-1"}, chartDefaults())

	assert.Equal(t, bucketName("team", "team-a", "orders"), ref.Name)
	assert.Equal(t, "eu-central-1", ref.Region)
	assert.Equal(t, "uid-1", ref.UID)
	assert.False(t, ref.Bound)
}

func TestBucketOfTakesTheDefaultsOfAnEmptyParameter(t *testing.T) {
	ref := bucketOf(request(), Params{}, chartDefaults())

	assert.Equal(t, bucketName(defaultBucketPrefix, "team-a", "orders"), ref.Name)
	assert.Equal(t, "eu-west-1", ref.Region)
}

func TestBucketOfPrefersTheRecordedRow(t *testing.T) {
	row := bucketRow("kept-bucket", "eu-north-1")
	row.ID = "arn:aws:s3:::kept-bucket"

	ref := bucketOf(request(row), Params{Region: "eu-central-1"}, chartDefaults())

	assert.Equal(t, "kept-bucket", ref.Name)
	assert.Equal(t, "eu-north-1", ref.Region)
	assert.True(t, ref.Bound)
}

func TestBucketOfBindsTheBucketOfARowWithoutARN(t *testing.T) {
	ref := bucketOf(request(bucketRow("bound-bucket", "eu-north-1")), Params{}, chartDefaults())

	assert.Equal(t, "bound-bucket", ref.Name)
	assert.Equal(t, "eu-north-1", ref.Region)
	assert.True(t, ref.Bound, "the plugin records a row before it creates the bucket")
}

func TestInUseRowsDropABindingThatNeverHadTheBucket(t *testing.T) {
	bound := bucketRow("b1", "eu-west-1")
	confirmed := bucketRow("b1", "eu-west-1")
	confirmed.ID = "arn:aws:s3:::b1"
	contract := pluginsv1alpha1.ResourceRef{Kind: kindObjectStorageConfig, Name: "orders-backup"}
	grant := pluginsv1alpha1.ResourceRef{Kind: "Grant", Name: "g"}

	owned := bucketInUseError{Bucket: "b1", Owner: "the request team-a/other"}

	assert.Equal(t, []pluginsv1alpha1.ResourceRef{contract}, inUseRows(request(bound, contract, grant), owned))
	assert.Equal(t, []pluginsv1alpha1.ResourceRef{confirmed, contract},
		inUseRows(request(confirmed, contract, grant), owned))
}

func TestInUseRowsKeepABindingWhenS3RefusesToShowTheBucket(t *testing.T) {
	bound := bucketRow("b1", "eu-west-1")
	unread := bucketInUseError{Bucket: "b1", Owner: "another AWS account", MaybeOwn: true}

	assert.Equal(t, []pluginsv1alpha1.ResourceRef{bound}, inUseRows(request(bound), unread),
		"the plugin can have created the bucket before S3 refused to show it")
}

func TestKeyOfAnotherRegionIsRefused(t *testing.T) {
	assert.Nil(t, keyInOtherRegion("", "eu-central-1"))
	assert.Nil(t, keyInOtherRegion(keyARN, "eu-west-1"))

	waiting := keyInOtherRegion(keyARN, "eu-central-1")
	require.NotNil(t, waiting)
	assert.Equal(t, pluginsv1alpha1.ReasonInvalidParameters, waiting.Reason)
	assert.Contains(t, waiting.Message, keyARN+" is in eu-west-1")
	assert.Contains(t, waiting.Message, "eu-central-1")

	notAKey := keyInOtherRegion("arn:aws:iam::111122223333:role/r", "eu-west-1")
	require.NotNil(t, notAKey)
	assert.Contains(t, notAKey.Message, "not the ARN of a KMS key")
}

func TestChangedBucketRefusesAnotherRegion(t *testing.T) {
	row := bucketRow("camunda-team-a-orders-0123456789abcdef", "eu-west-1")
	ref := bucketRef{Name: row.Name, Region: "eu-central-1"}

	waiting := changedBucket(&row, ref)

	require.NotNil(t, waiting)
	assert.Equal(t, pluginsv1alpha1.ReasonInvalidParameters, waiting.Reason)
	assert.Contains(t, waiting.Message, "camunda-team-a-orders-0123456789abcdef in eu-west-1")
}

func TestChangedBucketRefusesAnotherPrefix(t *testing.T) {
	row := bucketRow("camunda-team-a-orders-0123456789abcdef", "eu-west-1")

	waiting := changedBucket(&row, bucketRef{Name: "team-team-a-orders-0123456789abcdef", Region: "eu-west-1"})

	require.NotNil(t, waiting)
	assert.Contains(t, waiting.Message, "bucketPrefix and region cannot change")
}

func TestChangedBucketAcceptsTheSameBucketOrNoRow(t *testing.T) {
	row := bucketRow("b1", "eu-west-1")

	assert.Nil(t, changedBucket(&row, bucketRef{Name: "b1", Region: "eu-west-1"}))
	assert.Nil(t, changedBucket(nil, bucketRef{Name: "b1", Region: "eu-west-1"}))
}

func TestOutputsHoldKeyArnOnlyWithAKey(t *testing.T) {
	assert.Equal(t, map[string]string{
		"objectStorageConfig": "orders-backup",
		"bucket":              "b1",
		"bucketArn":           "arn:aws-cn:s3:::b1",
	}, outputs("orders-backup", "b1", "aws-cn", ""))

	assert.Equal(t, keyARN, outputs("orders-backup", "b1", "aws", keyARN)["keyArn"])
}

func TestContractNamesTheBucketAndItsRegion(t *testing.T) {
	ref := bucketRef{Name: "b1", Region: "eu-central-1"}

	got := contract(chartDefaults(), "orders-backup", ref, "")

	assert.Equal(t, "orders-backup", got.Name)
	assert.Equal(t, v1.ObjectStorageTypeS3, got.Spec.Type)
	require.NotNil(t, got.Spec.S3)
	assert.Equal(t, "b1", got.Spec.S3.BucketName)
	assert.Equal(t, "eu-central-1", got.Spec.S3.Region)
	assert.Equal(t, v1.ObjectStorageAuthTypeWorkloadIdentity, got.Spec.S3.Auth.Type)
}

func TestContractNamesTheRoleOnlyWithIRSA(t *testing.T) {
	ref := bucketRef{Name: "b1", Region: "eu-west-1"}
	irsa := chartDefaults()
	podIdentity := chartDefaults()
	podIdentity.Mode = awskit.ModePodIdentity

	assert.Equal(t, roleARN, contract(irsa, "r", ref, roleARN).Spec.S3.Auth.WorkloadIdentity.RoleARN)
	assert.Nil(t, contract(podIdentity, "r", ref, roleARN).Spec.S3.Auth.WorkloadIdentity,
		"a roleArn makes the IRSA token win over Pod Identity")
	assert.Nil(t, contract(irsa, "r", ref, "").Spec.S3.Auth.WorkloadIdentity)
}

func TestSummaryShowsTheSizeAsNotKnownYet(t *testing.T) {
	ref := bucketRef{Name: "b1", Region: "eu-west-1"}

	unknown := summary(ref, "", 0, false)
	known := summary(ref, "", 3<<30, true)

	require.Len(t, unknown, 4)
	assert.Equal(t, pluginsv1alpha1.SummaryItem{
		Label:    "Stored size (S3 Standard)",
		Value:    "Not known yet",
		Severity: pluginsv1alpha1.SeverityInfo,
	}, unknown[1])
	assert.Equal(t, "3Gi", known[1].Value)
}

func TestSummaryNamesTheBucketTheEncryptionAndTheRegion(t *testing.T) {
	ref := bucketRef{Name: "b1", Region: "eu-west-1"}

	withKey := summary(ref, keyARN, 0, false)
	withoutKey := summary(ref, "", 0, false)

	assert.Equal(t, pluginsv1alpha1.SummaryItem{Label: "Bucket", Value: "b1"}, withKey[0])
	assert.Equal(t, "SSE-KMS with an S3 Bucket Key, key "+keyARN, withKey[2].Value)
	assert.Equal(t, "SSE-S3", withoutKey[2].Value)
	assert.Equal(t, pluginsv1alpha1.SummaryItem{Label: "Region", Value: "eu-west-1"}, withKey[3])
}

func TestResourcesRecordTheBucketAndTheContract(t *testing.T) {
	rows := resources(chartDefaults(), bucketRef{Name: "b1", Region: "eu-west-1"}, request())

	assert.Equal(t, []pluginsv1alpha1.ResourceRef{
		{
			Kind:     kindS3Bucket,
			Name:     "b1",
			ID:       "arn:aws:s3:::b1",
			Location: "eu-west-1",
			Role:     "Backup storage of the cluster orders",
			OnOrphan: pluginsv1alpha1.OnOrphanKept,
		},
		{Kind: "ObjectStorageConfig", Name: "orders-backup", OnOrphan: pluginsv1alpha1.OnOrphanKept},
	}, rows)
}
