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
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pluginsv1alpha1 "github.com/konsole-is/camunda-operator/api/plugins/v1alpha1"
	v1 "github.com/konsole-is/camunda-operator/api/v1"

	"github.com/konsole-is/camunda-operator-plugins/pkg/awskit"
	"github.com/konsole-is/camunda-operator-plugins/pkg/pluginkit"
)

// The reasons that the plugin sets on a request, beside InvalidParameters.
const (
	// ReasonBucketNameInUse means that the bucket name of the request belongs
	// to another request, to no request, to another AWS account, or to a
	// bucket in another region. The outputs are not ready.
	ReasonBucketNameInUse = "BucketNameInUse"
	// ReasonRecordingBucket means that the plugin records the name and the
	// region of the bucket in status.resources, and creates the bucket at the
	// next reconcile.
	ReasonRecordingBucket = "RecordingBucket"
)

// The inputs and the outputs of a request, and the kinds of its rows of
// status.resources.
const (
	inputKey                  = "key"
	inputPrincipal            = "principal"
	outputObjectStorageConfig = "objectStorageConfig"
	outputBucket              = "bucket"
	outputBucketARN           = "bucketArn"
	outputKeyARN              = "keyArn"
	kindS3Bucket              = "S3Bucket"
	kindObjectStorageConfig   = "ObjectStorageConfig"
)

const (
	// defaultAbortDays is the abortIncompleteUploadDays of a request that
	// sets none.
	defaultAbortDays int32 = 7
	// inUseRequeue is the wait before a request whose bucket name is in use
	// reconciles again. S3 can show the tags of a new bucket a moment late.
	inUseRequeue = time.Minute
	// recordRequeue is the wait between the record of a bucket and its
	// create.
	recordRequeue = time.Second
)

// Params are the parameters of a request.
type Params struct {
	// BucketPrefix starts the bucket name. Empty is camunda.
	BucketPrefix string `json:"bucketPrefix,omitempty"`
	// Region is the region of the bucket. Empty is the region of the chart.
	Region string `json:"region,omitempty"`
	// AbortIncompleteUploadDays is the age at which S3 aborts an incomplete
	// multipart upload. Nil is 7.
	AbortIncompleteUploadDays *int32 `json:"abortIncompleteUploadDays,omitempty"`
}

// Plugin serves backup storage requests. Each request is one cluster: the
// plugin keeps one S3 bucket for it, bound to the request by the UID tag, and
// an ObjectStorageConfig that names the bucket.
type Plugin struct {
	settings   Settings
	buckets    buckets
	cloudWatch func(region string) awskit.CloudWatch
	// reader reads the ObjectStorageConfig of a deleted request as it is
	// now, and writer deletes it.
	reader client.Reader
	writer client.Writer
}

// Reconcile keeps the bucket of the request and publishes it with its
// ObjectStorageConfig. It is Pending with the reason BucketNameInUse when the
// bucket name belongs to another owner, with InvalidParameters when
// bucketPrefix or region names another bucket than the one that the request
// has or the key is in another region, and with RecordingBucket for the one
// reconcile that records a new bucket before it creates the bucket. A failed
// AWS call is an error. A failed read of the stored size only shows the size
// as not known. Within the settle time of a write of the plugin, S3 can show a
// setting late, so a setting that differs is not repaired: the request
// reconciles again when the settle time ends.
func (p *Plugin) Reconcile(
	ctx context.Context,
	req pluginkit.Request[Params, map[string]string],
) (pluginkit.Result, error) {
	ref := paramsRef(req.Object, req.Parameters, p.settings)
	recorded := recordedBucket(req.Object)
	if recorded != nil {
		if waiting := changedBucket(recorded, ref); waiting != nil {
			return pluginkit.Result{Pending: waiting, Resources: ownRows(req.Object)}, nil
		}
		ref.Bound = true
	}

	keyARN := req.Inputs[inputKey]
	if waiting := keyInOtherRegion(keyARN, ref.Region); waiting != nil {
		return pluginkit.Result{Pending: waiting, Resources: ownRows(req.Object)}, nil
	}

	// A create whose answer is lost leaves a bucket, so the name is recorded
	// first, and a later change of bucketPrefix or region is refused.
	if recorded == nil {
		err := p.buckets.claimable(ctx, ref)
		if inUse, ok := errors.AsType[bucketInUseError](err); ok {
			return inUseResult(inUse, req.Object, nil), nil
		}
		if err != nil {
			return pluginkit.Result{}, err
		}

		row := resources(p.settings, ref, req.Object)[0]
		row.ID = ""
		return pluginkit.Result{
			Pending: &pluginkit.Waiting{
				Reason: ReasonRecordingBucket,
				Message: fmt.Sprintf(
					"Recording the bucket %s in %s before the plugin creates it",
					ref.Name,
					ref.Region,
				),
			},
			Resources:    []pluginsv1alpha1.ResourceRef{row},
			RequeueAfter: recordRequeue,
		}, nil
	}

	w := want{Tags: bucketTags(p.settings, ref), KeyARN: keyARN, AbortDays: defaultAbortDays}
	if days := req.Parameters.AbortIncompleteUploadDays; days != nil {
		w.AbortDays = *days
	}
	served := prior{
		Served: req.Object.Status.Outputs[outputBucket] != "",
		KeyARN: req.Object.Status.Outputs[outputKeyARN],
	}

	actions, err := p.buckets.ensure(ctx, ref, w, served)
	corrected := record(req, actions)

	if inUse, ok := errors.AsType[bucketInUseError](err); ok {
		return inUseResult(inUse, req.Object, corrected), nil
	}
	settling, late := errors.AsType[settlingError](err)
	if late {
		req.Log.Info(
			"Did not repair a setting of the S3 bucket within the settle time of the last write",
			"bucket", ref.Name,
			"recheckAfter", settling.Left,
		)
	}
	if err != nil && !late {
		return pluginkit.Result{Corrected: corrected}, err
	}

	size, sizeKnown, err := bucketSize(ctx, p.cloudWatch(ref.Region), ref.Name, time.Now())
	if err != nil {
		req.Log.Error(err, "Could not read the stored size of the S3 bucket", "bucket", ref.Name)
	}

	link, err := awskit.BucketConsoleURL(p.settings.Partition, ref.Region, ref.Name)
	if err != nil {
		return pluginkit.Result{Corrected: corrected}, err
	}

	return pluginkit.Result{
		Outputs:      outputs(req.Object.Name, ref.Name, p.settings.Partition, keyARN),
		Objects:      []client.Object{contract(p.settings, req.Object.Name, ref, req.Inputs[inputPrincipal])},
		Summary:      summary(ref, keyARN, size, sizeKnown),
		Links:        []pluginsv1alpha1.Link{{Name: "S3 bucket", URL: link}},
		Resources:    resources(p.settings, ref, req.Object),
		RequeueAfter: settling.Left,
		Corrected:    corrected,
	}, nil
}

// bucketOf returns the bucket of the request: the recorded S3Bucket row of
// status.resources when there is one, else the bucket of params.
func bucketOf(request *pluginsv1alpha1.PluginRequest, params Params, s Settings) bucketRef {
	ref := paramsRef(request, params, s)
	if recorded := recordedBucket(request); recorded != nil {
		ref.Name, ref.Region, ref.Bound = recorded.Name, recorded.Location, true
	}
	return ref
}

// paramsRef returns the bucket that params name for the request.
func paramsRef(request *pluginsv1alpha1.PluginRequest, params Params, s Settings) bucketRef {
	claim := request.Spec.ClaimRef.Name
	return bucketRef{
		Namespace: request.Namespace,
		Claim:     claim,
		Request:   request.Name,
		UID:       string(request.UID),
		Name:      bucketName(cmp.Or(params.BucketPrefix, defaultBucketPrefix), request.Namespace, claim),
		Region:    cmp.Or(params.Region, s.Region),
	}
}

// recordedBucket returns the S3Bucket row of status.resources, or nil.
func recordedBucket(request *pluginsv1alpha1.PluginRequest) *pluginsv1alpha1.ResourceRef {
	i := slices.IndexFunc(request.Status.Resources, func(r pluginsv1alpha1.ResourceRef) bool {
		return r.Kind == kindS3Bucket
	})
	if i < 0 {
		return nil
	}
	return &request.Status.Resources[i]
}

// changedBucket returns a Waiting with InvalidParameters when recorded names
// another bucket or region than ref, and nil otherwise.
func changedBucket(recorded *pluginsv1alpha1.ResourceRef, ref bucketRef) *pluginkit.Waiting {
	if recorded == nil || recorded.Name == ref.Name && recorded.Location == ref.Region {
		return nil
	}
	return &pluginkit.Waiting{
		Reason: pluginsv1alpha1.ReasonInvalidParameters,
		Message: fmt.Sprintf(
			"The bucket of the request is %s in %s. bucketPrefix and region cannot change after the bucket exists",
			recorded.Name,
			recorded.Location,
		),
	}
}

// keyInOtherRegion returns a Waiting with InvalidParameters when keyARN is
// not the ARN of a KMS key, or names a key outside region, and nil otherwise.
// An empty keyARN is no key.
func keyInOtherRegion(keyARN, region string) *pluginkit.Waiting {
	if keyARN == "" {
		return nil
	}

	parsed, err := arn.Parse(keyARN)
	switch {
	case err != nil || parsed.Service != "kms":
		return &pluginkit.Waiting{
			Reason:  pluginsv1alpha1.ReasonInvalidParameters,
			Message: fmt.Sprintf("The key input %s is not the ARN of a KMS key", keyARN),
		}
	case parsed.Region != region:
		return &pluginkit.Waiting{
			Reason: pluginsv1alpha1.ReasonInvalidParameters,
			Message: fmt.Sprintf(
				"The KMS key %s is in %s, and the bucket is in %s. S3 encrypts a bucket only with a key of its region",
				keyARN,
				parsed.Region,
				region,
			),
		}
	}
	return nil
}

// inUseResult is the result of a request whose bucket name is in use.
func inUseResult(
	inUse bucketInUseError,
	request *pluginsv1alpha1.PluginRequest,
	corrected []string,
) pluginkit.Result {
	return pluginkit.Result{
		Pending:      &pluginkit.Waiting{Reason: ReasonBucketNameInUse, Message: inUse.Error()},
		Resources:    inUseRows(request, inUse),
		RequeueAfter: inUseRequeue,
		Corrected:    corrected,
	}
}

// inUseRows returns the rows of ownRows, without an S3Bucket row that has no
// ARN when inUse is sure of another owner: the request never had that bucket,
// so a changed bucketPrefix may free it from the name.
func inUseRows(request *pluginsv1alpha1.PluginRequest, inUse bucketInUseError) []pluginsv1alpha1.ResourceRef {
	if inUse.MaybeOwn {
		return ownRows(request)
	}
	return slices.DeleteFunc(ownRows(request), func(r pluginsv1alpha1.ResourceRef) bool {
		return r.Kind == kindS3Bucket && r.ID == ""
	})
}

// ownRows returns the rows of status.resources that the plugin wrote, so
// that a Pending result keeps the record of the bucket.
func ownRows(request *pluginsv1alpha1.PluginRequest) []pluginsv1alpha1.ResourceRef {
	return slices.DeleteFunc(slices.Clone(request.Status.Resources), func(r pluginsv1alpha1.ResourceRef) bool {
		return r.Kind != kindS3Bucket && r.Kind != kindObjectStorageConfig
	})
}

// record records one event for each action, and returns the corrections
// among them.
func record(req pluginkit.Request[Params, map[string]string], actions []action) []string {
	var corrected []string
	for _, a := range actions {
		req.Event(corev1.EventTypeNormal, a.Reason, a.Verb, "%s", a.Note)
		if a.Corrected != "" {
			corrected = append(corrected, a.Corrected)
		}
	}
	return corrected
}

// outputs returns the outputs of a request whose bucket is bucket in
// partition: keyArn only with keyARN.
func outputs(request, bucket, partition, keyARN string) map[string]string {
	out := map[string]string{
		outputObjectStorageConfig: request,
		outputBucket:              bucket,
		outputBucketARN:           bucketARN(partition, bucket),
	}
	if keyARN != "" {
		out[outputKeyARN] = keyARN
	}
	return out
}

func bucketARN(partition, bucket string) string {
	return "arn:" + partition + ":s3:::" + bucket
}

// contract returns the ObjectStorageConfig of the request for the bucket of
// ref. It names principal as the role of the consumers only with ModeIRSA,
// because with Pod Identity a role annotation makes the IRSA token win.
func contract(s Settings, request string, ref bucketRef, principal string) *v1.ObjectStorageConfig {
	auth := v1.S3StorageAuth{Type: v1.ObjectStorageAuthTypeWorkloadIdentity}
	if s.Mode == awskit.ModeIRSA && principal != "" {
		auth.WorkloadIdentity = &v1.S3WorkloadIdentity{RoleARN: principal}
	}

	return &v1.ObjectStorageConfig{
		ObjectMeta: metav1.ObjectMeta{Name: request},
		Spec: v1.ObjectStorageConfigSpec{
			Type: v1.ObjectStorageTypeS3,
			S3:   &v1.S3Storage{BucketName: ref.Name, Region: ref.Region, Auth: auth},
		},
	}
}

// labelStoredSize is the summary item of the size of the objects in the
// storage class S3 Standard, which the CloudWatch metric of bucketSize counts.
const labelStoredSize = "Stored size (S3 Standard)"

// summary returns the summary of the bucket of ref with the key keyARN. A
// size that is not known shows as "Not known yet".
func summary(ref bucketRef, keyARN string, size int64, sizeKnown bool) []pluginsv1alpha1.SummaryItem {
	stored := pluginsv1alpha1.SummaryItem{
		Label:    labelStoredSize,
		Value:    "Not known yet",
		Severity: pluginsv1alpha1.SeverityInfo,
	}
	if sizeKnown {
		stored = pluginsv1alpha1.SummaryItem{
			Label: labelStoredSize,
			Value: resource.NewQuantity(size, resource.BinarySI).String(),
		}
	}

	return []pluginsv1alpha1.SummaryItem{
		{Label: "Bucket", Value: ref.Name},
		stored,
		{Label: "Encryption", Value: encryptionOf(keyARN)},
		{Label: "Region", Value: ref.Region},
	}
}

// resources returns the rows of status.resources: the bucket, and the
// ObjectStorageConfig. Both stay with the deletion policy Orphan.
func resources(s Settings, ref bucketRef, request *pluginsv1alpha1.PluginRequest) []pluginsv1alpha1.ResourceRef {
	return []pluginsv1alpha1.ResourceRef{
		{
			Kind:     kindS3Bucket,
			Name:     ref.Name,
			ID:       bucketARN(s.Partition, ref.Name),
			Location: ref.Region,
			Role:     "Backup storage of the cluster " + request.Spec.Target.Name,
			OnOrphan: pluginsv1alpha1.OnOrphanKept,
		},
		{Kind: kindObjectStorageConfig, Name: request.Name, OnOrphan: pluginsv1alpha1.OnOrphanKept},
	}
}

// Delete deletes the objects of the bucket of the request for at most the
// delete budget. While objects remain, it returns pluginkit.Deleting, and the
// next pass goes on. Then it deletes the bucket and the ObjectStorageConfig.
// A bucket that is gone, or that is not the request's, is not an error.
func (p *Plugin) Delete(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	ref := bucketOf(req.Object, req.Parameters, p.settings)

	actions, remain, err := p.buckets.delete(ctx, ref)
	record(req, actions)
	switch {
	case err != nil:
		return err
	case remain:
		return pluginkit.Deleting{Message: "Deleting the objects of the bucket " + ref.Name}
	}

	// envtest has no garbage collector, so the plugin deletes the contract
	// itself.
	return pluginkit.DeleteOwned(ctx, p.reader, p.writer, req.Object, &v1.ObjectStorageConfig{}, req.Object.Name)
}

// Orphan keeps the bucket of the request with its objects, and removes the
// request tags and the UID tag from it.
func (p *Plugin) Orphan(ctx context.Context, req pluginkit.Request[Params, map[string]string]) error {
	actions, err := p.buckets.release(ctx, bucketOf(req.Object, req.Parameters, p.settings))
	record(req, actions)
	return err
}

// health checks that the AWS credentials can list the buckets of the
// account.
func (p *Plugin) health(ctx context.Context) error {
	_, err := p.buckets.s3(p.settings.Region).ListBuckets(ctx, &s3.ListBucketsInput{MaxBuckets: aws.Int32(1)})
	if err != nil {
		return fmt.Errorf("listing the S3 buckets: %w", err)
	}
	return nil
}
