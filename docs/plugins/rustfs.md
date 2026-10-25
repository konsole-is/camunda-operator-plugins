# RustFS bucket

The rustfs plugin serves the `backup-storage` section of the operator. It gives each cluster its own bucket on a [RustFS](https://rustfs.com/) server, and an access key that only that bucket accepts. The cluster writes its backups there. It serves through [pluginkit](../pluginkit.md).

The chart has one registration, `rustfs-bucket`.

## What it creates

For each request, the plugin creates these objects in RustFS:

- One bucket.
- One policy that allows the reads and writes of a backup on that bucket and its objects, and nothing else.
- One user with that policy. The access key ID of the user is the name of the bucket. The plugin makes a random secret key of 40 characters.

The bucket, the policy, and the user are three objects with the same name: `<namespace>-<request>-<hash>`, at most 63 characters. `<hash>` is 16 hex characters of a hash of the namespace and the request name. A dot in a name becomes a hyphen. The plugin finds each object again by this name, so it never makes a second bucket for one request.

Zeebe needs a backup bucket that no other cluster uses, so each cluster gets its own bucket. The key of one cluster cannot read or write the bucket of another cluster, or any other bucket.

In the namespace of the claim, the plugin writes:

- One Secret `<request>-rustfs` with the keys `accessKeyId` and `secretAccessKey`.
- One `ObjectStorageConfig` `<request>` of type `S3`. It names the bucket, the endpoint and the region of the chart values, path-style access, and the Secret, with `auth.type: credentials`. The Camunda pods of the cluster use this endpoint and this key for their backups.

The plugin also writes:

- One PluginRegistration for each item of the chart value `registrations`.
- One Lease `<release>` in the release namespace. When more than one replica runs, only the replica that holds the Lease writes.

The summary of a request shows the bucket, its used size, and the endpoint. When `rustfs.consoleUrl` is set, the request links to the RustFS console. `status.resources` lists the bucket, the user, the Secret, and the `ObjectStorageConfig`.

The used size comes from the scanner of RustFS, so it lags behind the writes. The plugin reads it again every 5 minutes.

While the Secret holds the secret key of the user, the plugin does not change the key. If someone deletes the Secret, the plugin sets a new secret key on the user and writes the Secret again. The old key then stops working.

## Prerequisites

Before you install the chart, make sure that you have:

1. A RustFS server of a tested release, `>=1.0.1 <1.1.0`. The plugin does not install or upgrade RustFS.
2. One S3 endpoint of RustFS that both the controller and the Camunda pods can reach.
3. A Secret in the release namespace with an admin access key of RustFS. This key must be able to create buckets, users, and policies. The default name is `rustfs-admin`, with the keys `accessKey` and `secretKey`.
4. The CRDs of the operator.

For example, this command makes the admin Secret:

```sh
kubectl create secret generic rustfs-admin --namespace camunda-operator-plugins \
  --from-literal=accessKey=<admin access key> --from-literal=secretKey=<admin secret key>
```

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `rustfs.endpoint` | `http://rustfs.rustfs.svc:9000` | The S3 endpoint of RustFS, an absolute `http` or `https` URL. It goes into every `ObjectStorageConfig`. |
| `rustfs.region` | `us-east-1` | The region of the S3 API. It goes into every `ObjectStorageConfig`. |
| `rustfs.adminSecret.name` | `rustfs-admin` | The Secret in the release namespace that holds the admin access key. |
| `rustfs.adminSecret.accessKeyKey` | `accessKey` | The key of the admin access key in that Secret. |
| `rustfs.adminSecret.secretKeyKey` | `secretKey` | The key of the admin secret key in that Secret. |
| `rustfs.consoleUrl` | `""` | The URL of the RustFS console. When it is set, each request links to it. |
| `registrations` | `rustfs-bucket` | The PluginRegistrations that the controller writes at startup and serves. Each item is a name and the fields of a registration spec. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one. The item `limits` shows the tested RustFS releases. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/rustfs` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

The controller can read and write every Secret and every `ObjectStorageConfig` of the cluster. pluginkit watches the objects that the controller writes, and this watch lists them in all namespaces.

## Parameters

The registration has no parameters.

## Outputs

| Key | Value |
| --- | --- |
| `objectStorageConfig` | The name of the `ObjectStorageConfig` of the request, in the namespace of the claim. |

## Grants

The plugin writes no grants.

## Deletion

The registration offers the deletion policies `Delete` and `Orphan`.

With `Delete`, the plugin deletes these, in this order:

1. The user, so that no key can write while the bucket empties.
2. The policy.
3. Each object of the bucket, and then the bucket.
4. The Secret and the `ObjectStorageConfig`.

CAUTION: Use `Delete` only when you do not need the backups. `Delete` erases each backup in the bucket.

With `Orphan`, the plugin keeps the bucket, the user, the policy, the Secret, and the `ObjectStorageConfig`. The Secret and the `ObjectStorageConfig` lose their owner reference to the request, and keep their labels.

The name of the bucket comes from the namespace and the name of the request. Thus a later request with the same namespace and name uses the kept bucket, user, and policy again, with the backups in the bucket. If that request has the policy `Delete`, its delete erases those backups too.

## Troubleshooting

| Reason | Condition | What to do |
| --- | --- | --- |
| `Unhealthy` | `Ready` of the registration | RustFS does not answer, or it refuses the admin key. The message gives the error. Make sure that `rustfs.endpoint` is correct, and that the admin Secret holds a valid admin key. |
| `ReconcileError` | `Ready` of the request | A call to RustFS failed. The message names the step and the call that RustFS refused, for example `RustFS refused add-user: AccessDenied: Access Denied.`. Make sure that the admin key can create buckets, users, and policies. pluginkit tries again. |
| `DeleteFailed` | `Ready` of a deleted request | The plugin could not delete an object in RustFS or in the cluster. The message gives the error. The finalizer stays until the delete succeeds, and pluginkit tries again. |

A message `bucket "<name>" exists and belongs to another RustFS user` means that a bucket of that name existed before the request. Delete or rename that bucket in RustFS.

The summary item `Used size` with the value `Not counted yet` means that the scanner of RustFS has not counted the bucket yet. The value `Unknown` means that the plugin could not read the size. The request is still `Ready`. The log of the controller gives the error.

When the controller Pod does not get ready, read its log. The controller stops when the settings are not valid: an empty or relative `rustfs.endpoint`, a relative `rustfs.consoleUrl`, or an empty admin key. It also stops when it cannot read the file of its registrations, or when the variable `POD_NAMESPACE` or `SERVICE_ACCOUNT_NAME` is not set.
