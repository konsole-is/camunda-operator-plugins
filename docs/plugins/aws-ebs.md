# AWS EBS encrypted volumes

The aws-ebs plugin serves the `volume-encryption` section of the operator. It gives each Camunda cluster one StorageClass of the [Amazon EBS CSI driver](https://github.com/kubernetes-sigs/aws-ebs-csi-driver) that encrypts each new EBS volume with the AWS KMS key of the cluster. It serves through [pluginkit](../pluginkit.md).

The chart installs one registration, `aws-ebs-encrypted`. It needs the key of the `encryption-key` section of the same claim, for example from the [AWS KMS key](aws-kms.md) plugin.

## What it creates

For each request, the plugin creates one StorageClass `camunda-<namespace>-<claim>-<hash>`. Each `.` of the claim becomes `-`. The hash is 16 hex digits of the namespace and the name of the request, so two claims never get the same StorageClass, also when their names read the same. A long namespace or claim is cut, so the name has at most 63 characters.

| StorageClass field | Value |
| --- | --- |
| `provisioner` | `ebs.csi.aws.com` |
| `parameters.type` | the parameter `type`, default `gp3` |
| `parameters.iops` | the parameter `iops`, only when it is set |
| `parameters.throughput` | the parameter `throughput`, only when it is set |
| `parameters.csi.storage.k8s.io/fstype` | the parameter `fsType`, default `ext4` |
| `parameters.encrypted` | `"true"` |
| `parameters.kmsKeyId` | the ARN of the key input |
| `reclaimPolicy` | the parameter `reclaimPolicy`, default `Delete` |
| `allowVolumeExpansion` | `true` |
| `volumeBindingMode` | `WaitForFirstConsumer` |

The StorageClass is cluster-scoped, so it has no owner reference. It has the labels `plugins.camunda.io/registration`, `plugins.camunda.io/request`, and `plugins.camunda.io/request-namespace`. The plugin changes or deletes only a StorageClass with all three labels of its request.

The plugin makes no AWS call, and its controller needs no IAM role. The EBS CSI driver creates and encrypts the volumes.

The plugin also writes:

- One PluginRegistration for each item of the chart value `registrations`.
- One Lease `<release>` in the release namespace. Only the replica that holds it writes.

The summary of a request shows the name of the StorageClass, the volume type, and the ARN of the key. The request links to the key in the KMS console.

## Prerequisites

Before you install the chart, make sure that you have:

1. An EKS cluster with the CRDs of the operator.
2. The EBS CSI driver, as the EKS add-on `aws-ebs-csi-driver` or from its Helm chart, with an IAM role of its own. Give that role the AWS managed policy `AmazonEBSCSIDriverPolicy`, through IRSA or a Pod Identity association of the ServiceAccount `ebs-csi-controller-sa`.
3. The right of that role to use each key. With the AWS KMS key plugin, set the chart value `csiDriverRoleArn` of the aws-kms chart to the ARN of the driver role. The plugin then writes a key policy that lets the driver encrypt with the key and create grants for the volumes. See [Key policy](aws-kms.md#key-policy). With keys of your own, give the driver role the same rights in the key policy of each key.
4. An option of the `encryption-key` section on each claim that selects `aws-ebs-encrypted`. The input `key` is required, so the fleet manager shows the option as not available without one.

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `registrations` | `aws-ebs-encrypted` | The PluginRegistrations that the controller writes at startup and serves. Each item is a name and the fields of a registration spec. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one. |
| `resync` | `1h` | The time after which the controller reconciles each request again, plus up to 10%. A StorageClass that someone deleted comes back within this time. `0` turns it off. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/aws-ebs` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

An install with the default registration:

```sh
helm install aws-ebs charts/aws-ebs --namespace camunda-operator-plugins
```

## Parameters

| Parameter | Values | Default | Meaning |
| --- | --- | --- | --- |
| `type` | `gp3`, `gp2`, `io1`, `io2`, `st1`, `sc1` | `gp3` | The EBS volume type. |
| `iops` | 100 or more | none | The IOPS of each volume. Only for `gp3`, `io1`, and `io2`. |
| `throughput` | 125 or more | none | The throughput of each volume in MiB/s. Only for `gp3`. |
| `fsType` | `ext4`, `xfs`, `ext3` | `ext4` | The file system of each volume. The StorageClass gets it as `csi.storage.k8s.io/fstype`. |
| `reclaimPolicy` | `Delete`, `Retain` | `Delete` | What happens to an EBS volume when its PersistentVolumeClaim is deleted. |

The schema refuses `throughput` with a `type` other than `gp3`, and `iops` with a `type` other than `gp3`, `io1`, or `io2`. A claim can change no parameter. The admin sets them in the `parameterValues` of the registration.

Kubernetes does not let a StorageClass change its parameters, its provisioner, or its reclaim policy. When one of them changes in the `parameterValues` of the registration, or when the key of the claim changes, the plugin deletes the StorageClass and creates it again with the same name. It records the event `StorageClassReplaced`, which names the changed fields. The name does not change, so the clusters keep their StorageClass. Volumes that exist keep their old type, file system, and key. Only new volumes get the new values.

## Outputs

| Key | Value | Read by |
| --- | --- | --- |
| `storageClassName` | The name of the StorageClass of the request. | The operator. It puts it in `spec.zeebe.storageClassName` of the `CamundaCluster`. The built-in `elasticsearch`, `database-server`, and `management-database-server` options read it as their input `storageClassName`. |

These volumes of the cluster use the StorageClass:

- The volumes of the Zeebe brokers.
- The volumes of the Elasticsearch or PostgreSQL server of the cluster, when the claim selects a built-in `elasticsearch` or `database-server` option.
- The volumes of the PostgreSQL server of the management plane, when its claim selects the built-in `management-database-server` option.

`spec.zeebe.storageClassName` cannot change after the `CamundaCluster` exists. Select the option when you create the claim.

## Grants

The plugin writes no Grants. The registration has no grant templates. The EBS CSI driver uses the key with its own role, through the key policy. The pods of the cluster never use the key.

## Deletion

The registration offers the deletion policies `Delete` and `Orphan`.

| Policy | What happens |
| --- | --- |
| `Delete` | Deletes the StorageClass and records the event `StorageClassDeleted`. |
| `Orphan` | Keeps the StorageClass with its three labels. |

With both policies, the plugin does not touch the volumes. A PersistentVolume and its EBS volume stay until their PersistentVolumeClaim is deleted. Then the `reclaimPolicy` of the PersistentVolume decides: `Delete` deletes the EBS volume, and `Retain` keeps it. A volume keeps working after its StorageClass is deleted.

## Troubleshooting

| Reason | Condition | What to do |
| --- | --- | --- |
| `MissingInput` | `OutputsReady` and `Ready` of the request | The claim has no option of the `encryption-key` section, or that option has no key yet. Select a key option on the claim, or wait until the key request is `Ready`. |
| `InvalidKey` | `OutputsReady` and `Ready` of the request | The input `key` is not the ARN of a KMS key. The message names the value. Select a key option that publishes a KMS key ARN, such as the AWS KMS key plugin. |
| `StorageClassNameTaken` | `OutputsReady` and `Ready` of the request | A StorageClass of the name of the request exists without the labels of the request, for example one made by hand. The plugin never changes or deletes it. Check that no PersistentVolumeClaim needs it, then delete it, and the plugin creates its own StorageClass. |
| `InvalidParameters` | `OutputsReady` and `Ready` of the request | The parameters do not match the schema of the registration. The message names the field or the rule. Correct the `parameterValues` of the registration. |
| `ReconcileError` | `Ready` of the request | The plugin could not read, delete, or apply the StorageClass. The message gives the error. pluginkit tries again. |
| `DeleteFailed` | `Ready` of a deleted request | The StorageClass of the request still exists, for example because a finalizer holds it. The message names it. The finalizer of the request stays, and pluginkit tries again. |

When someone deletes the StorageClass, the plugin creates it again at the next resync: within the time of the chart value `resync`, plus up to 10%.

When a PersistentVolumeClaim stays `Pending` and its events show a KMS error, such as an access denied error, the driver role cannot use the key. Check that `csiDriverRoleArn` of the aws-kms chart names the role of the EBS CSI driver, and that the key is `Enabled`.

When the controller Pod does not get ready, read its log with `kubectl logs deployment/<release> -n <namespace>`. The controller stops when it cannot read the file of its registrations, or when the variable `POD_NAMESPACE` or `SERVICE_ACCOUNT_NAME` is not set.
