# AWS plugins

The AWS plugins are five controllers in one image and one Helm chart, `aws`. You turn on each controller that you need in the values of the chart. This page tells how to install the chart, the values that all controllers share, and the IAM role of the controllers. The page of each controller tells what it creates, its own values, and the IAM policy that it needs.

## What it runs

One release of the chart runs one Pod, in a Deployment with the name of the release. The Pod runs each controller that is on, and no other.

| Values block | Controller | Registrations | Page |
| --- | --- | --- | --- |
| `identity` | aws-identity | `aws-irsa`, or `aws-pod-identity` with `mode: podIdentity` | [AWS identity](aws-identity.md) |
| `connectorsRole` | aws-connectors-role | `aws-connectors-role`, `aws-connectors-existing-role` | [AWS Connectors role](aws-connectors-role.md) |
| `kms` | aws-kms | `aws-kms-key`, `aws-kms-key-multi-region` | [AWS KMS key](aws-kms.md) |
| `ebs` | aws-ebs | `aws-ebs-encrypted` | [AWS EBS encrypted volumes](aws-ebs.md) |
| `s3` | aws-s3 | `aws-s3` | [AWS S3 bucket](aws-s3.md) |

The `registrations` value of each block lists the registrations of its controller. The table gives the defaults. The page of each controller tells when to change them.

## Install

Before you install, read the Prerequisites of the page of each controller that you turn on, and create the [role of the controllers](#the-role-of-the-controllers).

This `values.yaml` turns on aws-identity and aws-kms with IRSA. aws-identity needs `identity.permissionsBoundaryArn`, `identity.allowedActions`, and with IRSA `oidcProviderArn`. aws-kms needs a region, from `region` or from the `AWS_REGION` of the pod.

```yaml
region: eu-west-1
oidcProviderArn: arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/aws-plugins-controller
identity:
  enabled: true
  permissionsBoundaryArn: arn:aws:iam::111122223333:policy/camunda-boundary
  allowedActions: ["s3:*", "kms:Decrypt", "kms:GenerateDataKey"]
kms:
  enabled: true
```

Install the chart with the file. `<version>` is the version of a release of this repository.

```sh
helm install aws oci://ghcr.io/konsole-is/camunda-operator-plugins/charts/aws \
  --version <version> --namespace camunda-operator-plugins --create-namespace -f values.yaml
```

A controller that is off starts nothing, writes no registration, and needs no IAM permission. In the example, aws-connectors-role, aws-ebs, and aws-s3 are off.

Every controller is off by default. With no controller on, the install fails with the message `turn on at least one controller: identity.enabled, connectorsRole.enabled, kms.enabled, ebs.enabled, or s3.enabled`. The chart also refuses a block that is on without registrations, and a registration name in two blocks that are on.

The chart does not check every value. One value that is missing or not valid, in the block of a controller that is on, stops the whole Pod. The log of the Pod names the controller and the value:

```sh
kubectl logs deployment/aws -n camunda-operator-plugins
```

Correct the value in `values.yaml`, and upgrade the release with `helm upgrade` and the same arguments as the install.

## Shared values

These values are at the top level of the values, and every controller that is on reads them. The values of one controller are in its block, and its page lists them. A cluster in this table is a Camunda cluster that a claim of the operator makes.

| Value | Default | Meaning |
| --- | --- | --- |
| `mode` | `irsa` | How the pods of a cluster assume their AWS roles: `irsa` (IAM roles for service accounts) or `podIdentity` (EKS Pod Identity). aws-identity, aws-connectors-role, and aws-s3 read it. |
| `region` | `""` | The AWS region of the API calls, of the KMS keys, and of a bucket whose request sets no region. Empty takes the region of the pod, such as `AWS_REGION`. When the pod has no region either, aws-kms and aws-s3 do not start, and aws-connectors-role does not start with `podIdentity`. |
| `extraTags` | `{}` | Tags that go on each AWS object of the controllers, next to the tags of the request, for example `cost-center: platform`. A key cannot start with `aws:` or `camunda.io/`. |
| `oidcProviderArn` | `""` | The IAM OIDC provider of the EKS cluster. aws-identity and aws-connectors-role need it with `irsa`. |
| `eksClusterName` | `""` | The EKS cluster of the Pod Identity associations. aws-identity and aws-connectors-role need it with `podIdentity`. |
| `endpointURL` | `""` | Sends every AWS call to this endpoint instead of the endpoints of AWS. |
| `serviceAccount.annotations` | `{}` | The annotations of the ServiceAccount of the release, for example `eks.amazonaws.com/role-arn` of the [role of the controllers](#the-role-of-the-controllers). |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/aws` | The image of the controllers. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `image.pullPolicy` | `IfNotPresent` | The pull policy of the image. |
| `imagePullSecrets` | `[]` | The pull secrets of the Pod. |
| `resources` | 10m CPU and 96Mi memory requested, 256Mi memory limit | The resources of the controller container, which runs every controller that is on. |

## The role of the controllers

The release has one ServiceAccount, with the name of the release: `aws` for the release `aws`. All controllers of the release run as one IAM role. Give that role to the ServiceAccount in one of these ways:

- IRSA: the trust policy of the role names the IAM OIDC provider of the EKS cluster, with the `sub` value `system:serviceaccount:camunda-operator-plugins:aws`. `serviceAccount.annotations` sets `eks.amazonaws.com/role-arn` to the ARN of the role.
- EKS Pod Identity: a Pod Identity association of the ServiceAccount `aws` in `camunda-operator-plugins` to the role. Leave `serviceAccount.annotations` empty.

The value `mode` does not decide this. It is how the pods of the clusters get their roles.

Attach the policy of each controller that is on to the role. Make each policy a managed policy of its own:

- aws-identity: [Prerequisites](aws-identity.md#prerequisites)
- aws-connectors-role: [Prerequisites](aws-connectors-role.md#prerequisites)
- aws-kms: [Prerequisites](aws-kms.md#prerequisites)
- aws-s3: [Prerequisites](aws-s3.md#prerequisites)

aws-ebs makes no AWS call and needs no policy. With only aws-ebs on, the Pod loads no AWS config and needs no role, no region, and no credentials.

## More than one release

You can install the chart twice in one namespace, with other controllers on in each release. For example, keep aws-kms in an IAM role of its own, in the release `aws-keys`. Each release has its own ServiceAccount, IAM role, and leader election Lease, all named after the release. So the role of `aws-keys` trusts the ServiceAccount `aws-keys`, and has only the policy of aws-kms. Each release also has its own shared values, such as `region`.

The `values-keys.yaml` of the release `aws-keys` turns on only `kms`:

```yaml
region: eu-west-1
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/aws-keys-controller
kms:
  enabled: true
```

In the `values.yaml` of the release `aws`, set `kms.enabled: false`. Then upgrade `aws`, install `aws-keys`, and remove the policy of aws-kms from the role of `aws`:

```sh
helm upgrade aws oci://ghcr.io/konsole-is/camunda-operator-plugins/charts/aws \
  --version <version> --namespace camunda-operator-plugins -f values.yaml
helm install aws-keys oci://ghcr.io/konsole-is/camunda-operator-plugins/charts/aws \
  --version <version> --namespace camunda-operator-plugins -f values-keys.yaml
```

Never turn on one controller in two releases. To move a controller to the other release, first turn it off in its release, or uninstall that release. Then turn it on in the other release, with the same values in its block. The requests of the controller wait between the two steps, and keep their AWS objects.

## Turn a controller off

Set `enabled: false` in the block of the controller, and upgrade the release with the file:

```sh
helm upgrade aws oci://ghcr.io/konsole-is/camunda-operator-plugins/charts/aws \
  --version <version> --namespace camunda-operator-plugins -f values.yaml
```

The PluginRegistrations that the controller wrote stay, and so do the AWS objects of its requests. Nothing serves the requests until you turn the controller on again. PluginRegistrations are cluster-scoped. Delete one with `kubectl delete pluginregistration <name>` when no claim uses it.

## Move from the five charts

Before this chart, each AWS controller had its own chart: `aws-identity`, `aws-connectors-role`, `aws-kms`, `aws-ebs`, and `aws-s3`. These steps move to the chart `aws` and keep every key, role, bucket, and StorageClass. The clusters keep working during the move. Only new claims, and changes and deletes of claims, wait.

1. Write the `values.yaml` of the chart `aws` from the values of each old release. Get them with `helm get values <release> --namespace camunda-operator-plugins`.
    - Put each value of a controller under the block of that controller, as the table below shows, and set `enabled: true` in each block. Write the shared values once.
    - Keep the same `roleNamePrefix` of `identity` and `connectorsRole`, the same `kms.aliasPrefix`, `region`, and `s3.partition`, and the whole `registrations` list of each old release. A changed role name prefix or region makes the controllers create new AWS objects.
    - When the old releases used different regions, install one release of the chart `aws` for each region. See [More than one release](#more-than-one-release).
2. Give the ServiceAccount `aws` the [role of the controllers](#the-role-of-the-controllers), with the policies of the controllers that are on, and set its IRSA annotation in `values.yaml`.
3. Uninstall the old releases, and wait until their Pods are gone:

    ```sh
    helm uninstall aws-identity aws-connectors-role aws-kms aws-ebs aws-s3 \
      --namespace camunda-operator-plugins --wait
    ```

    The uninstall keeps every registration, request, Grant, and AWS object. Nothing serves the requests until step 4.

4. Install the chart `aws`. At startup, each controller writes its registrations again, with the ServiceAccount `aws`:

    ```sh
    helm install aws oci://ghcr.io/konsole-is/camunda-operator-plugins/charts/aws \
      --version <version> --namespace camunda-operator-plugins -f values.yaml
    ```

5. Check the result. Each registration names the ServiceAccount `aws` and is `Ready`, and each request is `Ready` with the outputs that it had before the move:

    ```sh
    kubectl get pluginregistrations \
      -o custom-columns='NAME:.metadata.name,SERVICE ACCOUNT:.spec.serviceAccount.name,READY:.status.conditions[?(@.type=="Ready")].status'
    kubectl get pluginrequests -A
    ```

The old values go to these new values:

| Old release | Old values | New values |
| --- | --- | --- |
| each | `mode`, `region`, `extraTags`, `oidcProviderArn`, `eksClusterName`, `endpointURL`, `imagePullSecrets` | the same names, once |
| `aws-identity` | `permissionsBoundaryArn`, `roleNamePrefix`, `allowedActions`, `resync`, `registrations` | `identity.<value>` |
| `aws-connectors-role` | `permissionsBoundaryArn`, `roleNamePrefix`, `resync`, `registrations` | `connectorsRole.<value>` |
| `aws-kms` | `aliasPrefix`, `keyAdministratorArns`, `csiDriverRoleArn`, `resync`, `registrations` | `kms.<value>` |
| `aws-ebs` | `resync`, `registrations` | `ebs.<value>` |
| `aws-s3` | `partition`, `deleteBudget`, `resync`, `registrations` | `s3.<value>` |
| each | `image`, `resources`, `serviceAccount.annotations` | not copied |
