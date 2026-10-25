# AWS identity

The aws-identity plugin serves the `cloud-identity` section of the operator. It gives each Camunda cluster on EKS one IAM role, and the pods of the cluster run as that role. Other plugins do not create roles. They write Grants, and this plugin puts each allowed Grant on the role of its cluster. It serves through [pluginkit](../pluginkit.md).

The chart installs one registration, by the chart value `mode`:

- `aws-irsa` with `mode: irsa`: the pods assume the role through IAM roles for service accounts (IRSA).
- `aws-pod-identity` with `mode: podIdentity`: the pods assume the role through EKS Pod Identity.

## What it creates

For each request, the plugin creates one IAM role:

- The name is `camunda-<hash>`, with 16 hex digits of a hash of the namespace and the claim. Two claims never share a role.
- The permissions boundary is the chart value `permissionsBoundaryArn`, on every role.
- The tags `camunda.io/namespace`, `camunda.io/claim`, and `camunda.io/request` name the claim and the request.
- The role trusts `spec.target.serviceAccountName` of the request (the ServiceAccount of the cluster, `<claim>-camunda`) and each name in `spec.target.additionalServiceAccounts` (for example `<claim>-es-es` of the built-in Elasticsearch). It trusts no other ServiceAccount. The claim controller fills both fields. The plugin does not add names itself.
- With IRSA, the trust policy names the OIDC provider of the EKS cluster, with one `sub` value for each ServiceAccount.
- With Pod Identity, the trust policy names `pods.eks.amazonaws.com` with `sts:AssumeRole` and `sts:TagSession`. The plugin creates one Pod Identity association for each ServiceAccount, with the same tags.

When a ServiceAccount leaves the request, the plugin removes it from the trust policy, and with Pod Identity it deletes its association.

The plugin also writes:

- One PluginRegistration for each item of the chart value `registrations`.
- The conditions of each Grant of its clusters (see [Grants](#grants)).
- One Lease `<release>` in the release namespace. Only the replica that holds it writes.

The summary of a request shows the role name, the number of applied grants, the size of the inline policies against the IAM limit of 10,240 characters, and the permissions boundary. The size has the severity `Warning` above 80% of the limit. The request links to the role in the IAM console.

## Prerequisites

Before you install the chart, make sure that you have:

1. An EKS cluster, and the CRDs of the operator in it.
2. For IRSA: the IAM OIDC provider of the EKS cluster, and its ARN, such as `arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE`.
3. For Pod Identity: the EKS Pod Identity Agent add-on in the EKS cluster, and the name of the EKS cluster.
4. A managed policy for the permissions boundary. It holds the most that any role of a Camunda cluster may do, for example the S3 and KMS actions of your buckets and keys.
5. A list of the IAM actions of that boundary, as patterns such as `s3:*` or `kms:Decrypt`, for the chart value `allowedActions`.
6. An IAM role for the controller itself, with the permissions below. Give it to the controller through `serviceAccount.annotations` (IRSA) or a Pod Identity association of the ServiceAccount of the release.

The controller role needs this policy. Replace the account, the region, the EKS cluster, and the boundary ARN. Remove the last two statements with IRSA.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "RolesWithTheBoundary",
      "Effect": "Allow",
      "Action": ["iam:CreateRole", "iam:PutRolePermissionsBoundary", "iam:PutRolePolicy"],
      "Resource": "arn:aws:iam::111122223333:role/camunda-*",
      "Condition": {
        "StringEquals": {"iam:PermissionsBoundary": "arn:aws:iam::111122223333:policy/camunda-boundary"}
      }
    },
    {
      "Sid": "Roles",
      "Effect": "Allow",
      "Action": [
        "iam:GetRole", "iam:UpdateAssumeRolePolicy", "iam:TagRole", "iam:DeleteRole",
        "iam:GetRolePolicy", "iam:ListRolePolicies", "iam:DeleteRolePolicy",
        "iam:ListAttachedRolePolicies", "iam:DetachRolePolicy"
      ],
      "Resource": "arn:aws:iam::111122223333:role/camunda-*"
    },
    {
      "Sid": "Health",
      "Effect": "Allow",
      "Action": ["iam:GetPolicy"],
      "Resource": "arn:aws:iam::111122223333:policy/camunda-boundary"
    },
    {
      "Sid": "Caller",
      "Effect": "Allow",
      "Action": ["sts:GetCallerIdentity"],
      "Resource": "*"
    },
    {
      "Sid": "PodIdentityPassRole",
      "Effect": "Allow",
      "Action": ["iam:PassRole"],
      "Resource": "arn:aws:iam::111122223333:role/camunda-*"
    },
    {
      "Sid": "PodIdentityAssociations",
      "Effect": "Allow",
      "Action": [
        "eks:CreatePodIdentityAssociation", "eks:ListPodIdentityAssociations",
        "eks:DescribePodIdentityAssociation", "eks:UpdatePodIdentityAssociation",
        "eks:DeletePodIdentityAssociation", "eks:TagResource"
      ],
      "Resource": [
        "arn:aws:eks:eu-west-1:111122223333:cluster/my-eks",
        "arn:aws:eks:eu-west-1:111122223333:podidentityassociation/my-eks/*"
      ]
    }
  ]
}
```

The condition on the boundary makes sure that the controller cannot create a role, or give a role a policy, without the boundary.

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `mode` | `irsa` | `irsa` or `podIdentity`. The chart refuses another value. |
| `permissionsBoundaryArn` | `""` | Required. The managed policy that bounds every role. The registration shows it as a limit. |
| `allowedActions` | `[]` | Required. The IAM action patterns that a Grant may hold, such as `s3:*`. `*` and `?` are wildcards. The registration shows them as a limit. |
| `oidcProviderArn` | `""` | Required with IRSA. The IAM OIDC provider of the EKS cluster. |
| `eksClusterName` | `""` | Required with Pod Identity. The EKS cluster of the associations. |
| `region` | `""` | The AWS region. Empty takes the region of the pod, such as `AWS_REGION`. |
| `endpointURL` | `""` | Sends every AWS call to this endpoint, for example LocalStack. |
| `serviceAccount.annotations` | `{}` | The annotations of the ServiceAccount of the controller, for example `eks.amazonaws.com/role-arn` of the controller role. |
| `registrations` | `[{name: aws-irsa}]` | The PluginRegistrations that the controller writes at startup and serves. Each item is a name and fields of a registration spec, merged over the defaults of `mode`. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one, `aws-pod-identity` with `mode: irsa`, and `aws-irsa` with `mode: podIdentity`. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/aws-identity` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

The chart renders without the required values, but the controller does not start without them. Its log names the missing value.

An install with IRSA:

```sh
helm install aws-identity charts/aws-identity --namespace camunda-operator-plugins \
  --set permissionsBoundaryArn=arn:aws:iam::111122223333:policy/camunda-boundary \
  --set 'allowedActions={s3:*,kms:Decrypt,kms:GenerateDataKey}' \
  --set oidcProviderArn=arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE \
  --set serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn=arn:aws:iam::111122223333:role/aws-identity-controller
```

An install with Pod Identity:

```sh
helm install aws-identity charts/aws-identity --namespace camunda-operator-plugins \
  --set mode=podIdentity --set 'registrations[0].name=aws-pod-identity' \
  --set permissionsBoundaryArn=arn:aws:iam::111122223333:policy/camunda-boundary \
  --set 'allowedActions={s3:*,kms:Decrypt,kms:GenerateDataKey}' \
  --set eksClusterName=my-eks
```

## Parameters

The registrations have no parameters. A claim changes nothing.

## Outputs

| Key | Value | Read by |
| --- | --- | --- |
| `principal` | The ARN of the role, format `aws-iam-role-arn`. | other plugins, for example a bucket whose consumers annotate their own ServiceAccounts |
| `serviceAccountAnnotations` | A JSON object. With IRSA `{"eks.amazonaws.com/role-arn": "<role ARN>"}`, with Pod Identity `{}`. | the cluster claim, which puts the annotations on the ServiceAccounts of the cluster and of the built-in storage |

## Grants

The plugin writes no Grants of its own. It reads the Grants of other plugins and puts them on the role:

- It reads each Grant in the namespace of the request whose label `plugins.camunda.io/cluster` names the cluster of the request. It ignores a Grant whose request is gone or belongs to another claim.
- A Grant with the format `aws-iam-statement` becomes one inline policy `grant-<Grant name>` on the role, with the fragment as its one statement.
- The plugin refuses a statement with an action outside `allowedActions`, with `NotAction`, or without `Action`. An action is read as text: the pattern `s3:*` allows the action `s3:Get*`, and the pattern `s3:GetObject` does not.
- IAM allows 10,240 characters, white space not counted, for all inline policies of a role together. The plugin applies the Grants in the order of their creation, and refuses a Grant that would take the role over the limit.
- A refused Grant that was never applied puts nothing on the role.
- An applied Grant stays applied. When its fragment changes and the new fragment is refused, or its template changes in the registration, the role keeps the statement that it had. Only a deleted Grant loses its policy.
- A policy on the role whose name does not start with `grant-` is not the plugin's. The plugin does not change it, but it counts towards the limit.

The plugin writes three conditions on each Grant:

| Condition | `True` | `False` reasons |
| --- | --- | --- |
| `Applied` | The role has the statement. | `OverCeiling`, `OverSizeLimit`, `Rejected` |
| `MatchesTemplate` | The fragment is the template of the registration of the Grant, filled in for its request now. | `TemplateChanged` |
| `Ready` | Both are `True`. | The reason and message of `Applied` when it is `False`, else those of `MatchesTemplate`. |

A request whose Grant has `Ready=False` gets `Ready=False` with the reason `GrantRejected`. Its outputs stay ready.

## Deletion

The registrations offer the deletion policies `Delete` and `Orphan`.

| Policy | IRSA | Pod Identity |
| --- | --- | --- |
| `Delete` | Deletes the inline policies and the role. | Deletes the associations, the inline policies, and the role. |
| `Orphan` | Keeps the role and its inline policies. | Keeps the role and its inline policies, and deletes the associations, because they name a namespace that goes away. |

`Delete` also deletes the inline policies that an admin added to the role by hand, and detaches the managed policies that an admin attached. An orphaned role keeps its tags, so you can find it by the namespace and the claim.

## Troubleshooting

| Reason | Condition | What to do |
| --- | --- | --- |
| `MissingServiceAccount` | `OutputsReady` and `Ready` of the request | The request names no ServiceAccount in `spec.target`. The claim controller sets it. Check the claim and the operator version. |
| `ReconcileError` | `Ready` of the request | An AWS call failed. The message gives the call and the AWS error, for example `AccessDenied` when the controller role lacks a permission above. With Pod Identity, the message can say that a ServiceAccount has an association for a role of another claim: the plugin does not move it. Delete that association, or rename the ServiceAccount. pluginkit tries again. |
| `DeleteFailed` | `Ready` of a deleted request | The plugin could not delete the role or an association. The message gives the AWS error. The finalizer stays, and pluginkit tries again. |
| `Unhealthy` | `Ready` of the registration | The controller cannot call STS with its credentials, or it cannot read the permissions boundary. The message gives the error. Check the controller role and `permissionsBoundaryArn`. |
| `OverCeiling` | `Applied` and `Ready` of a Grant | The message names the action outside `allowedActions`. Add the action to the boundary and to `allowedActions`, or change the grant template of the other plugin. |
| `OverSizeLimit` | `Applied` and `Ready` of a Grant | The inline policies of the role would pass 10,240 characters. Remove a Grant of the cluster, or make the grant templates shorter. |
| `Rejected` | `Applied` and `Ready` of a Grant | The fragment is not one IAM statement that the plugin can check, its format is not `aws-iam-statement`, or IAM refused the policy. The message says which. Correct the grant template of the other plugin. |
| `TemplateChanged` | `MatchesTemplate` and `Ready` of a Grant | The fragment of the Grant is not its template filled in now, for example after a registration change. The role keeps the statement. The plugin of the Grant writes it again on its next reconcile. |

When the controller Pod does not get ready, read its log. The controller stops when a required chart value is missing or not valid, when it cannot read the file of its registrations, or when the variable `POD_NAMESPACE` or `SERVICE_ACCOUNT_NAME` is not set.
