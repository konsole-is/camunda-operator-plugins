# AWS KMS key

The aws-kms plugin serves the `encryption-key` section of the operator. It gives each Camunda cluster one customer-managed AWS KMS key with automatic rotation. Other sections, such as encrypted volumes and an encrypted backup bucket, read the ARN of the key from the outputs of the request. It serves through [pluginkit](../pluginkit.md).

The chart installs two registrations:

- `aws-kms-key`: one key in the region of the plugin.
- `aws-kms-key-multi-region`: one multi-Region key in the region of the plugin, and a replica of it in each region of the parameter `replicaRegions`.

## What it creates

For each request, the plugin creates one symmetric encryption key (`SYMMETRIC_DEFAULT`, `ENCRYPT_DECRYPT`, key material from KMS):

- Automatic rotation is on.
- The alias is `alias/<aliasPrefix>/<namespace>/<claim>`. Each character of the namespace or the claim other than a letter, a digit, `_`, or `-` becomes `_`. An alias longer than the 256 characters of KMS is cut, and ends with `-` and 8 hex digits of a hash.
- The tags `camunda.io/namespace`, `camunda.io/claim`, and `camunda.io/request` name the claim and the request. The tags of the chart value `extraTags` go on the key too.
- The description is `Camunda cluster <namespace>/<claim>`.
- The key policy is the default key policy of KMS, unless the chart sets `keyAdministratorArns` or `csiDriverRoleArn` (see [Key policy](#key-policy)).

With `aws-kms-key-multi-region`, the key is a multi-Region primary key. The plugin creates one replica in each region of `replicaRegions`, with the same tags, the same description, and the same key policy. KMS rotates the replicas with the primary key.

The plugin finds the key of a request by its alias. A request whose status write was lost does not get a second key.

The plugin also writes:

- One PluginRegistration for each item of the chart value `registrations`.
- One Lease `<release>` in the release namespace. Only the replica that holds it writes.

The summary of a request shows the key state, the rotation, the alias, and the state of each replica. A replica outside `replicaRegions` shows as a `Warning`. A key that is pending deletion shows as an `Error`, with its deletion date once KMS gives one. A multi-Region primary key gets its date only after its replicas are deleted. The request links to the key in the KMS console.

## Prerequisites

Before you install the chart, make sure that you have:

1. A Kubernetes cluster with the CRDs of the operator.
2. An IAM role for the controller, with the permissions below. Give it to the controller through `serviceAccount.annotations` (IRSA) or a Pod Identity association of the ServiceAccount of the release.

The controller role needs this policy. Replace the account, the regions, and `camunda`, the alias prefix. The statement `Aliases` names the region of the plugin only, because the plugin writes aliases only there. The statements `Keys` and `KeyPolicy` name the region of the plugin and each replica region. Without the multi-Region registration, remove the replica regions. Without `keyAdministratorArns` and `csiDriverRoleArn`, remove the statement `KeyPolicy`.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "CreateAndList",
      "Effect": "Allow",
      "Action": ["kms:CreateKey", "kms:TagResource", "kms:ListAliases"],
      "Resource": "*"
    },
    {
      "Sid": "Aliases",
      "Effect": "Allow",
      "Action": ["kms:CreateAlias", "kms:DeleteAlias"],
      "Resource": [
        "arn:aws:kms:eu-west-1:111122223333:alias/camunda/*",
        "arn:aws:kms:eu-west-1:111122223333:key/*"
      ]
    },
    {
      "Sid": "Keys",
      "Effect": "Allow",
      "Action": [
        "kms:DescribeKey", "kms:ListResourceTags", "kms:TagResource", "kms:UntagResource",
        "kms:GetKeyRotationStatus", "kms:EnableKeyRotation", "kms:ReplicateKey", "kms:ScheduleKeyDeletion"
      ],
      "Resource": [
        "arn:aws:kms:eu-west-1:111122223333:key/*",
        "arn:aws:kms:eu-central-1:111122223333:key/*"
      ]
    },
    {
      "Sid": "KeyPolicy",
      "Effect": "Allow",
      "Action": ["kms:GetKeyPolicy", "kms:PutKeyPolicy"],
      "Resource": [
        "arn:aws:kms:eu-west-1:111122223333:key/*",
        "arn:aws:kms:eu-central-1:111122223333:key/*"
      ]
    }
  ]
}
```

KMS refuses to create a key with a key policy that does not let the caller change that policy later. So with policy values, the controller role needs `kms:PutKeyPolicy` also to create a key. The statement `KeyAdministrators` below gives it that when the controller role is one of `keyAdministratorArns`. Else the statement `AccountRoot` gives it, through the IAM policy above.

## Key policy

When `keyAdministratorArns` and `csiDriverRoleArn` are both empty, each key keeps the default key policy of KMS: the account root may do every action, so IAM policies can grant the use of the key. The plugin then never reads or writes a key policy.

When one of the two values is set, the plugin writes this key policy on each key and each replica:

| Sid | Principal | Actions | When |
| --- | --- | --- | --- |
| `AccountRoot` | the account root | `kms:*` | always |
| `KeyAdministrators` | `keyAdministratorArns` | `kms:Create*`, `kms:Describe*`, `kms:Enable*`, `kms:List*`, `kms:Put*`, `kms:Update*`, `kms:Revoke*`, `kms:Disable*`, `kms:Get*`, `kms:Delete*`, `kms:TagResource`, `kms:UntagResource`, `kms:ScheduleKeyDeletion`, `kms:CancelKeyDeletion`, `kms:ReplicateKey` | `keyAdministratorArns` is not empty |
| `EBSCSIDriverUse` | `csiDriverRoleArn` | `kms:Encrypt`, `kms:Decrypt`, `kms:ReEncrypt*`, `kms:GenerateDataKey*`, `kms:DescribeKey` | `csiDriverRoleArn` is set |
| `EBSCSIDriverGrants` | `csiDriverRoleArn` | `kms:CreateGrant`, only when `kms:GrantIsForAWSResource` is `true` | `csiDriverRoleArn` is set |

The EBS CSI driver needs `csiDriverRoleArn` to create volumes that are encrypted with the key, such as the volumes of the `aws-ebs-encrypted` option.

When you change a policy value, the plugin writes the new key policy on each key at its next reconcile, at the latest after 10 minutes. When you clear both values, the plugin stops writing key policies, and each existing key keeps the policy that it has.

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `region` | `""` | The AWS region of the keys. Empty takes the region of the pod, such as `AWS_REGION`. The controller does not start without a region. |
| `aliasPrefix` | `camunda` | The prefix of each alias, after `alias/`. 1 to 128 characters of `A-Z`, `a-z`, `0-9`, `/`, `_`, and `-`, not `aws`, not starting with `aws/` or `alias/`, and not starting or ending with `/`. The registrations show it as a limit. |
| `extraTags` | `{}` | Tags for each key and each replica, such as a cost center. A key must not start with `aws:` or `camunda.io/`. A value must not hold a comma. |
| `keyAdministratorArns` | `[]` | The IAM principals that administer each key. See [Key policy](#key-policy). |
| `csiDriverRoleArn` | `""` | The IAM role of the EBS CSI driver. See [Key policy](#key-policy). |
| `endpointURL` | `""` | Sends every AWS call to this endpoint instead of the endpoints of AWS. |
| `serviceAccount.annotations` | `{}` | The annotations of the ServiceAccount of the controller, for example `eks.amazonaws.com/role-arn` of the controller role. |
| `registrations` | `aws-kms-key` and `aws-kms-key-multi-region` | The PluginRegistrations that the controller writes at startup and serves. Each item is a name and the fields of a registration spec. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/aws-kms` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

The registration `aws-kms-key-multi-region` requires `replicaRegions`. Set the regions for every claim in the `parameterValues` of the registration, for example with a values file:

```yaml
region: eu-west-1
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/aws-kms-controller
registrations:
  - name: aws-kms-key
    # The other fields of the default aws-kms-key item of values.yaml.
  - name: aws-kms-key-multi-region
    # The other fields of the default aws-kms-key-multi-region item of values.yaml.
    parameterValues:
      replicaRegions: [eu-central-1]
```

A list in a values file replaces the default list, so copy each item of `registrations` from the `values.yaml` of the chart and change it there. Then install with the file:

```sh
helm install aws-kms charts/aws-kms --namespace camunda-operator-plugins -f my-values.yaml
```

An install with the default registrations, without replica regions:

```sh
helm install aws-kms charts/aws-kms --namespace camunda-operator-plugins \
  --set region=eu-west-1 \
  --set serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn=arn:aws:iam::111122223333:role/aws-kms-controller
```

## Parameters

| Parameter | Registrations | Meaning |
| --- | --- | --- |
| `pendingWindowDays` | both | The days between the delete of the request and the delete of the key, 7 to 30. The default is 30. |
| `replicaRegions` | `aws-kms-key-multi-region`, required | The regions of the replicas, at least one. The region of the plugin is refused with `InvalidParameters`. |

When you remove a region from `replicaRegions`, the plugin keeps the replica in that region until the request is deleted. The summary shows it as a `Warning`. A key that the plugin created for `aws-kms-key` stays a single-Region key: a request that asks for replicas for it gets `KeyNotMultiRegion`.

## Outputs

| Key | Value | Read by |
| --- | --- | --- |
| `keyRef` | The ARN of the key, of the primary key for a multi-Region key, format `aws-kms-arn`. | the sections that encrypt with the key, such as volume encryption and backup storage |
| `format` | `aws-kms-arn` | the same sections, to know how to read `keyRef` |

## Grants

The plugin writes no Grants. The registrations have no grant templates. Give the pods that use the key access through the IAM role of the cluster, or through the key policy.

## Deletion

The registrations offer the deletion policies `Delete` and `Orphan`.

| Policy | What happens |
| --- | --- |
| `Delete` | Schedules the deletion of each replica and of the key after `pendingWindowDays`, and deletes the alias at once. A new claim of the same name gets a new key. |
| `Orphan` | Keeps the key and each replica enabled, and keeps the alias. Removes the three `camunda.io/` tags, and keeps the tags of `extraTags`. |

**A deleted key makes every volume and every object that it encrypted unreadable, for good.** From the start of the window, KMS refuses every use of the key, so the data is unreadable at once. At the end of the window, KMS deletes the key. During the window, you can cancel the deletion in the KMS console, then enable the key again. A key that is pending deletion has no alias. Find it by its tags `camunda.io/namespace` and `camunda.io/claim`, or by its description `Camunda cluster <namespace>/<claim>`. KMS deletes a multi-Region primary key only after its replicas.

Find an orphaned key by its alias. While the kept key has the alias, a new claim of the same name in the same namespace reports `KeyAliasInUse` and gets no key. Delete the alias, or move it to another key, to let the new claim create one.

When the controller stops between the create of a key and the create of its alias, a key with the request tags and no alias stays. When the alias fails, the plugin schedules the deletion of that key after 7 days.

## Troubleshooting

| Reason | Condition | What to do |
| --- | --- | --- |
| `InvalidParameters` | `OutputsReady` and `Ready` of the request | The parameters do not match the schema of the registration, or `replicaRegions` holds the region of the plugin. The message says which. Correct the parameters of the claim or the `parameterValues` of the registration. |
| `KeyAliasInUse` | `OutputsReady` and `Ready` of the request | The alias of the claim names a key of another request, or a key that an `Orphan` deletion kept. The message names the alias, the key, and its request. Delete the alias or move it to another key. |
| `KeyPendingDeletion` | `Ready` of the request | Someone scheduled the deletion of the key outside the plugin. The outputs stay. Cancel the deletion in the KMS console and enable the key, or delete the claim. |
| `KeyNotEnabled` | `Ready` of the request | The key is in another state than `Enabled`, for example `Disabled`. The message names the state. Enable the key in the KMS console. |
| `ReplicasPending` | `Ready` of the request | KMS is still creating a replica. The plugin checks again after 5 seconds. KMS usually needs less than a minute. When it stays, the summary shows the state of each replica: check the replica in the KMS console of its region. |
| `KeyNotMultiRegion` | `Ready` of the request | The request asks for replicas, and its key is a single-Region key, for example after a change of the registration. The outputs stay. Remove `replicaRegions`, or delete the alias to let the plugin create a multi-Region key. |
| `ReconcileError` | `Ready` of the request | An AWS call failed. The message gives the call and the AWS error, usually `AccessDeniedException` when the controller role lacks a permission above. pluginkit tries again. |
| `DeleteFailed` | `Ready` of a deleted request | The plugin could not schedule the deletion or remove the tags, a replica is still being created, or `pendingWindowDays` is outside 7 to 30. The message gives the error. The finalizer stays, and pluginkit tries again. |
| `Unhealthy` | `Ready` of the registration | The controller cannot list the aliases of its region with its credentials. The message gives the error. Check the controller role and `kms:ListAliases`. |

When the controller Pod does not get ready, read its log with `kubectl logs deployment/<release> -n <namespace>`. The controller stops when it finds no region, when `aliasPrefix`, `extraTags`, `keyAdministratorArns`, or `csiDriverRoleArn` is not valid, when it cannot read the file of its registrations or call STS, or when the variable `POD_NAMESPACE` or `SERVICE_ACCOUNT_NAME` is not set.
