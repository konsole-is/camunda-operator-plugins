# AWS S3 bucket

The aws-s3 plugin serves the `backup-storage` section of the operator. It gives each Camunda cluster its own S3 bucket for its backups, and an `ObjectStorageConfig` that names the bucket. Zeebe needs a bucket that no other Zeebe cluster writes to, so one bucket serves one cluster. It serves through [pluginkit](../pluginkit.md).

The chart installs one registration, `aws-s3`. It reads two optional inputs: the KMS key of the `encryption-key` section, and the IAM role of the `cloud-identity` section. The operator writes one PluginRequest in the namespace of each claim that picks this option, and deletes it with the claim. The request takes the deletion policy of the claim.

## What it creates

For each request, the plugin creates one general purpose bucket in the region of the request:

- The name is `<bucketPrefix>-<namespace>-<claim>-<hash>`, at most 63 characters. Each `.` of the namespace or the claim becomes `-`. A name that is too long is cut before the hash. The hash is 16 hex digits of `<bucketPrefix>/<namespace>/<claim>`, so two prefixes or claims whose names read the same after the cut, or such as `a-b/c` and `a/b-c`, get two names.
- The tags `camunda.io/namespace`, `camunda.io/claim`, and `camunda.io/request` name the claim and the request. The tag `camunda.io/request-uid` holds the UID of the PluginRequest. The tags of the chart value `extraTags` go on the bucket too.
- The public access block has all four flags on. AWS turns them on for a new bucket. The plugin reads the block at each reconcile, and writes it only when a flag is off or the block is missing.
- The default encryption is SSE-KMS with the key of the `key` input and an S3 Bucket Key. Without a key, it is SSE-S3. The `key` input must be the ARN of a key, not of an alias, in the region of the bucket.
- The lifecycle rule `abort-incomplete-uploads` aborts an incomplete multipart upload after `abortIncompleteUploadDays` days. The plugin keeps each other lifecycle rule of the bucket.
- Versioning stays off. The plugin never writes a bucket policy: pods get access to the bucket through the grants (see [Grants](#grants)).

The plugin also writes one `ObjectStorageConfig` named after the request, in the namespace of the request. It has the type `S3`, the bucket, the region, and `auth.type: workloadIdentity`. With the chart value `mode: irsa`, `auth.workloadIdentity.roleArn` is the role of the `principal` input. With `mode: podIdentity`, it names no role, because a role annotation on the ServiceAccount makes the IRSA token win over Pod Identity.

Before the plugin creates a bucket, it records the name and the region of the bucket in `status.resources` of the request. Then the request reports `RecordingBucket` for about one second. A bucket is bound to the PluginRequest that created it, by its tag `camunda.io/request-uid`. The plugin never uses, changes, or deletes a bucket with another UID or with no UID: the request reports `BucketNameInUse`. Bucket names are global across all AWS accounts. Two Kubernetes clusters with the same namespace and claim get the same name when they use the same `bucketPrefix`, also in two accounts. Give each Kubernetes cluster its own `bucketPrefix` (see [Parameters](#parameters)).

At each reconcile, and at the latest after `resync`, the plugin writes the tags, the public access block, the default encryption, and the abort rule again when someone changed them. A repair of the public access block, the encryption, or the abort rule records the event `DriftCorrected` on the request.

S3 can show a bucket setting late after a write. So for 5 minutes after the plugin writes to a bucket, it writes no setting that differs, except a changed `key` input. When the 5 minutes end, the request reconciles again, and the plugin writes each setting that still differs. A change of `abortIncompleteUploadDays` in these 5 minutes also waits until they end. The plugin keeps the time of its writes in memory. After a restart, the 5 minutes start at the first reconcile of each request.

The plugin also writes:

- One PluginRegistration for each item of the chart value `registrations`.
- One Lease `<release>` in the release namespace. Only the replica that holds it writes.

The summary of a request shows the bucket, the stored size, the encryption, and the region. The stored size is the latest daily `BucketSizeBytes` metric of the bucket in CloudWatch, for the storage class S3 Standard only. An object that a lifecycle rule moved to another storage class does not count. It is `Not known yet` when CloudWatch has no data point of the last three days, or when the controller role cannot read CloudWatch. The request links to the bucket in the S3 console.

## Prerequisites

Before you install the chart, make sure that you have:

1. A Kubernetes cluster with the CRDs of the operator.
2. An IAM role for the controller, with the permissions below. Give it to the controller through `serviceAccount.annotations` (IRSA), or through a Pod Identity association of the ServiceAccount `<release>` in the release namespace. With Pod Identity, leave `serviceAccount.annotations` empty.
3. For the grants, the [aws-identity](aws-identity.md) plugin. It gives the pods of each cluster their IAM role, also the association with Pod Identity, and puts the grants on that role. Its `allowedActions` must allow the actions of both grants: `s3:ListBucket`, `s3:GetBucketLocation`, `s3:ListBucketMultipartUploads`, `s3:ListBucketVersions`, `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts`, `kms:GenerateDataKey`, and `kms:Decrypt`.

The controller role needs this policy. Replace `camunda` with your `bucketPrefix`. For each other prefix that your claims use, add one resource to the statement `Buckets` and one to the statement `Objects`. In the partition `aws-cn` or `aws-us-gov`, replace `arn:aws:` with `arn:aws-cn:` or `arn:aws-us-gov:`.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Buckets",
      "Effect": "Allow",
      "Action": [
        "s3:CreateBucket",
        "s3:TagResource",
        "s3:GetBucketTagging",
        "s3:PutBucketTagging",
        "s3:GetBucketPublicAccessBlock",
        "s3:PutBucketPublicAccessBlock",
        "s3:GetEncryptionConfiguration",
        "s3:PutEncryptionConfiguration",
        "s3:GetLifecycleConfiguration",
        "s3:PutLifecycleConfiguration",
        "s3:GetBucketVersioning",
        "s3:ListBucket",
        "s3:ListBucketVersions",
        "s3:DeleteBucket"
      ],
      "Resource": "arn:aws:s3:::camunda-*"
    },
    {
      "Sid": "Objects",
      "Effect": "Allow",
      "Action": ["s3:DeleteObject", "s3:DeleteObjectVersion"],
      "Resource": "arn:aws:s3:::camunda-*/*"
    },
    {
      "Sid": "Health",
      "Effect": "Allow",
      "Action": "s3:ListAllMyBuckets",
      "Resource": "*"
    },
    {
      "Sid": "StoredSize",
      "Effect": "Allow",
      "Action": "cloudwatch:GetMetricStatistics",
      "Resource": "*"
    }
  ]
}
```

The controller needs no KMS permission: it only names the key in the default encryption of the bucket. `s3:TagResource` lets the plugin create a bucket with its tags. The statement `Objects` and the actions `s3:GetBucketVersioning` and `s3:ListBucketVersions` empty a bucket on a delete. Without the statement `StoredSize`, the summary shows the stored size as `Not known yet`, and everything else works. When a boundary denies `s3:PutBucketPublicAccessBlock`, the plugin works while the four flags stay on.

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `region` | `""` | The AWS region of the bucket of a request that sets no `region` parameter. Empty takes the region of the pod, such as `AWS_REGION`. The controller does not start without a region. |
| `partition` | `aws` | The AWS partition of the buckets: `aws`, `aws-cn`, or `aws-us-gov`. It goes into the bucket ARN of the outputs, which the grants read, and into the console link. |
| `mode` | `irsa` | How the pods of a cluster get their AWS role: `irsa` or `podIdentity`. Only `irsa` puts the role of the `principal` input into the `ObjectStorageConfig`. Use the mode of the aws-identity plugin. |
| `extraTags` | `{}` | Tags for each bucket, such as a cost center. At most 46, because a bucket also carries four `camunda.io/` tags. A key must not start with `aws:` or `camunda.io/`, and must not hold a `=`. A value must not hold a comma. |
| `deleteBudget` | `15s` | The time for which one pass of a delete deletes objects. See [Deletion](#deletion). |
| `resync` | `1h` | The time after which the controller reconciles each request again, and repairs a bucket setting that was changed outside the cluster. `0` turns it off. |
| `endpointURL` | `""` | Sends every AWS call to this endpoint instead of the endpoints of AWS. |
| `serviceAccount.annotations` | `{}` | The annotations of the ServiceAccount of the controller, for example `eks.amazonaws.com/role-arn` of the controller role. |
| `registrations` | `aws-s3` | The PluginRegistrations that the controller writes at startup and serves. Each item is a name and the fields of a registration spec. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/aws-s3` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

An install with the default registration:

```sh
helm install aws-s3 charts/aws-s3 --namespace camunda-operator-plugins \
  --set region=eu-west-1 \
  --set serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn=arn:aws:iam::111122223333:role/aws-s3-controller
```

To set a `bucketPrefix` for every claim of a Kubernetes cluster, copy the item `aws-s3` of `registrations` from the `values.yaml` of the chart into a values file, and add `parameterValues`. A list in a values file replaces the default list, so the copy must hold every field of the item. An Environment of the operator can override `parameterValues`.

```yaml
region: eu-west-1
registrations:
  - name: aws-s3
    # The other fields of the default aws-s3 item of values.yaml.
    parameterValues:
      bucketPrefix: prod-eu
```

## Parameters

| Parameter | Meaning |
| --- | --- |
| `bucketPrefix` | The start of each bucket name. The default is `camunda`. 1 to 40 characters of `a-z`, `0-9`, and `-`, which start and end with a letter or a digit. S3 reserves the name prefixes `xn--`, `sthree-`, and `amzn-s3-demo-`, and the name continues with `-` after `bucketPrefix`, so `sthree` and `amzn-s3-demo` are refused too. |
| `region` | The region of the bucket. Empty is the chart value `region`. |
| `abortIncompleteUploadDays` | The days after which S3 aborts an incomplete multipart upload, at least 1. The default is 7. |

`bucketPrefix` and `region` cannot change after the plugin recorded the bucket, which it does before it creates the bucket. The status of the request records the name and the region of its bucket. When a later value names another bucket, the request reports `InvalidParameters`, the backups stay in their bucket, and the plugin creates no second bucket. A bucket in another region needs a new claim. The plugin copies no backups into the bucket of the new claim.

Give each Kubernetes cluster its own `bucketPrefix` when two clusters can have a claim of the same name in the same namespace. Else the second cluster finds the bucket of the first, and its request reports `BucketNameInUse`.

## Outputs

| Key | Value | Read by |
| --- | --- | --- |
| `objectStorageConfig` | The name of the `ObjectStorageConfig`, in the namespace of the request. | the operator, which gives the backup storage of the cluster to Zeebe and the other consumers |
| `bucket` | The name of the bucket. | the summary and the admin |
| `bucketArn` | The ARN of the bucket, in the partition of the chart. | the grant `bucket-read-write` |
| `keyArn` | The ARN of the key of the default encryption, format `aws-kms-arn`. Only with the `key` input. | the grant `bucket-key-use` |

## Grants

The plugin writes these Grants for the IAM role of the cluster. The aws-identity plugin puts them on the role.

| Grant | Actions | Resource | When |
| --- | --- | --- | --- |
| `bucket-read-write` | `s3:ListBucket`, `s3:GetBucketLocation`, `s3:ListBucketMultipartUploads`, `s3:ListBucketVersions`, `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts` | the bucket and its objects | always |
| `bucket-key-use` | `kms:GenerateDataKey`, `kms:Decrypt` | the key of the `key` input | only with the `key` input |

When the `key` input changes, the plugin sets the default encryption to the new key, and the grant `bucket-key-use` names only the new key. Objects that S3 wrote before keep the old key. The pods of the cluster then cannot read them, unless the role of the cluster can still use the old key.

## Deletion

The registration offers the deletion policies `Delete` and `Orphan`.

| Policy | What happens |
| --- | --- |
| `Delete` | Deletes each object of the bucket, also each object version and each delete marker when someone turned versioning on. Then deletes the bucket and the `ObjectStorageConfig`. **The backups are gone for good.** The finalizer of the request stays until the bucket is gone. |
| `Orphan` | Keeps the bucket with its objects and its settings, and keeps the `ObjectStorageConfig` without its owner reference. Removes the four `camunda.io/` tags from the bucket, and keeps the tags of `extraTags`. |

The delete of a large bucket takes several passes. S3 deletes at most 1,000 keys with one call, and about 3,500 objects per second for each prefix. So one million objects take at least about five minutes. One pass deletes objects for about `deleteBudget`, and the next pass starts about one second later. Meanwhile `Ready` of the request is `False` with reason `Deleting` and the message `Deleting the objects of the bucket <name>`. The message gives no count of the objects that are left. A stop or a restart of the controller ends the pass, and does not lose progress: the next pass goes on with the objects that are left.

`Delete` deletes nothing when the bucket does not carry the UID of the request, for example a bucket of another Kubernetes cluster. The request then goes away, and the bucket stays.

S3 can show the tags of a new bucket late. While the recorded bucket shows no UID tag, `Delete` and `Orphan` wait, with reason `Deleting` and the message `Waiting for S3 to show the tags of the new bucket <name>`. The finalizer stays. The wait ends when the tag shows, or 5 minutes after the last write of the plugin to the bucket. Then a bucket without the UID tag stays. The plugin keeps the time of its writes in memory. After a restart of the controller, the 5 minutes start at the first pass that reads the bucket. It also keeps an `ObjectStorageConfig` without an owner reference to the request, such as the contract that an `Orphan` kept.

A new request has a new UID, also a request of the same claim after an `Orphan` deletion or after a restore of the namespace from a backup. While the old bucket has the name, the new request reports `BucketNameInUse`. To give the old bucket to the new request, tag the bucket with `camunda.io/request-uid` set to the UID of the new PluginRequest:

```sh
# The request of the claim for the backup storage:
kubectl get pluginrequests -n <namespace> -l plugins.camunda.io/section=backup-storage
uid=$(kubectl get pluginrequest <request> -n <namespace> -o jsonpath='{.metadata.uid}')
aws s3api get-bucket-tagging --bucket <bucket> --region <region>
# Add camunda.io/request-uid=$uid to the tag set that the command above shows, then write all of it:
aws s3api put-bucket-tagging --bucket <bucket> --region <region> \
  --tagging 'TagSet=[{Key=camunda.io/request-uid,Value='"$uid"'},...]'
```

`put-bucket-tagging` replaces every tag of the bucket, so write the other tags again with it. The plugin then adds its other tags, and writes the `ObjectStorageConfig` of the same name again, at the next reconcile. Never give away a bucket that a request of another Kubernetes cluster still uses: two Zeebe clusters must not write to one bucket.

## Troubleshooting

| Reason | Condition | What to do |
| --- | --- | --- |
| `BucketNameInUse` | `OutputsReady` and `Ready` of the request | The bucket name belongs to another owner. The message names the bucket and the owner: a request with its UID, `no request` for a bucket without the tag `camunda.io/request-uid` such as an orphaned bucket, another AWS account or a bucket that the controller may not read, or a bucket of the same name in another region. When the bucket is yours, check that the controller role has `s3:ListBucket` on it. Set another `bucketPrefix` in the `parameterValues` of the registration (see [Chart values](#chart-values)), give the bucket to the request (see [Deletion](#deletion)), or delete the bucket with its backups. The plugin checks again each minute. |
| `InvalidParameters` | `OutputsReady` and `Ready` of the request | The parameters do not match the schema, `bucketPrefix` or `region` changed after the plugin recorded the bucket, or the `key` input is not a key ARN or is in another region than the bucket. The message says which, and names the bucket and its region, or the key and its region. Set the parameters back, or give the claim a key of the region of the bucket. |
| `RecordingBucket` | `OutputsReady` and `Ready` of the request | The plugin recorded the bucket in the status, and creates it at the next reconcile, after about one second. Nothing to do. |
| `WaitingForBucketTags` | `OutputsReady` and `Ready` of the request | S3 does not show the tag `camunda.io/request-uid` of the new bucket yet. The plugin changes nothing on the bucket and checks again within 10 seconds. If the tag does not show within 5 minutes, the request reports `BucketNameInUse`. Nothing to do. |
| `ReconcileError` | `Ready` of the request | An AWS call failed. The message gives the call and the AWS error, usually `AccessDenied` when the controller role lacks a permission above. pluginkit tries again. |
| `Deleting` | `Ready` of a deleted request | The plugin is deleting the objects of the bucket in passes, or it waits for S3 to show the tags of a new bucket. Nothing to do. |
| `DeleteFailed` | `Ready` of a deleted request | The plugin could not empty or delete the bucket. The message gives the error. For an object that S3 refused to delete, it names the key and the code, for example `AccessDenied` when the role lacks `s3:DeleteObjectVersion`, or a code of S3 Object Lock. For an object under S3 Object Lock, wait until its retention ends, or remove its legal hold. When S3 refuses to show a bucket that the status of the request records, the delete fails with `AccessDenied` too. Give the controller role `s3:ListBucket` on that bucket. The finalizer stays, and pluginkit tries again. |
| `Unhealthy` | `Ready` of the registration | The controller cannot list the buckets of the account with its credentials. The message gives the error. Check the controller role and `s3:ListAllMyBuckets`. |

The stored size shows `Not known yet` for about one day after the create of a bucket, because S3 sends `BucketSizeBytes` to CloudWatch once a day. When it stays, give the controller role `cloudwatch:GetMetricStatistics`, and read the controller log for the error of CloudWatch.

When the controller Pod does not get ready, read its log with `kubectl logs deployment/<release> -n <namespace>`. The controller stops when it finds no region, when `partition` is not `aws`, `aws-cn`, or `aws-us-gov`, when `mode`, `extraTags`, or `deleteBudget` is not valid, when it cannot read the file of its registrations, or when the variable `POD_NAMESPACE` or `SERVICE_ACCOUNT_NAME` is not set.
