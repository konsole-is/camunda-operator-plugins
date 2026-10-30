# AWS Connectors role

The aws-connectors-role plugin serves the `connectors-permissions` section of the operator. It gives the Connectors of each Camunda cluster on EKS an IAM role of their own, apart from the role of the cluster. The admin decides what that role can do. The people who create clusters pick from named options and never see a role ARN. It serves through [pluginkit](../pluginkit.md).

The chart installs two registrations:

- `aws-connectors-role`, label "Role for each cluster": the plugin makes one IAM role for the Connectors of each cluster, with the policies that the admin allows.
- `aws-connectors-existing-role`, label "Existing role": the Connectors of each cluster use one IAM role that the admin made. The plugin calls no IAM API.

The chart value `mode` sets how the Connectors pods assume the role: `irsa` (IAM roles for service accounts) or `podIdentity` (EKS Pod Identity).

## Why the Connectors have their own role

An AWS connector, such as the SQS, S3, Lambda, or Bedrock connector, with the authentication **Default Credentials Chain (Hybrid/Self-Managed only)** uses the credentials of the AWS SDK default chain. In a Connectors pod on EKS, that chain gives the IAM role of the pod, through IRSA or EKS Pod Identity. Every process that runs a connector with this option acts as that role.

The role of the cluster, from [aws-identity](aws-identity.md), holds the access of the cluster itself, for example to its backup bucket. A process author must not get that access through a connector. So the Connectors run under the ServiceAccount `<claim>-connectors`, and this plugin gives that ServiceAccount a role with only the access that the admin gives to process authors.

The Connectors read documents through the Orchestration REST API, not from the document store. Do not give the Connectors role access to the document store.

## What it creates

For each request of `aws-connectors-role`, the plugin creates one IAM role:

- The name is `<roleNamePrefix>-<hash>`, with 16 hex digits of a hash of the EKS cluster (its OIDC issuer with IRSA, its region and name with Pod Identity), the namespace, and the claim.
- The permissions boundary is the chart value `permissionsBoundaryArn`. No Environment and no claim can change it.
- The tags `camunda.io/namespace`, `camunda.io/claim`, and `camunda.io/request` name the claim and the request. The tags of the chart value `extraTags` go on the role too.
- The role trusts only `spec.target.serviceAccountName` of the request, which is `<claim>-connectors`. It never trusts the ServiceAccount of the cluster, `<claim>-camunda`, or another name in `spec.target.additionalServiceAccounts`.
- With IRSA, the trust policy names the OIDC provider of the EKS cluster, with one `sub` value.
- With Pod Identity, the trust policy names `pods.eks.amazonaws.com` with `sts:AssumeRole` and `sts:TagSession`. The plugin creates one Pod Identity association for `<claim>-connectors`, with the same tags.
- The inline policy `connectors` is the parameter `inlinePolicy`, with its placeholders filled in.
- The managed policies are the parameters `managedPolicyArns` and `extraPolicyArns`.

The plugin owns every managed policy attachment of the role. On each reconcile, it detaches a policy that the parameters do not name, also one that someone attached by hand. Of the inline policies, it owns only `connectors`.

For each request of `aws-connectors-existing-role`, the plugin creates nothing in IAM. With Pod Identity, it creates one association of `<claim>-connectors` to the role of `roleArn`.

The plugin also writes:

- One PluginRegistration for each item of the chart value `registrations`.
- One Lease `<release>` in the release namespace. Only the replica that holds it writes.

The summary of an `aws-connectors-role` request shows the role name, the number of managed policies, the size of the inline policy against the IAM limit of 10,240 characters, and the permissions boundary. The size has the severity `Warning` above 80% of the limit. The summary of an `aws-connectors-existing-role` request shows the role ARN. Each request links to its role in the IAM console.

## Prerequisites

Before you install the chart, make sure that you have:

1. An EKS cluster, and the CRDs of the operator in it.
2. For IRSA: the IAM OIDC provider of the EKS cluster, and its ARN.
3. For Pod Identity: the EKS Pod Identity Agent add-on in the EKS cluster, and the name of the EKS cluster.
4. For `aws-connectors-role`: a managed policy for the permissions boundary. It holds the most that the Connectors of any cluster may do.
5. For `aws-connectors-role`: the managed policies that you offer to the Connectors.
6. For `aws-connectors-existing-role`: the role of the Connectors. Its trust policy names `system:serviceaccount:<namespace>:<claim>-connectors` of each cluster that uses it (IRSA), or `pods.eks.amazonaws.com` (Pod Identity).
7. An IAM role for the controller itself, with the permissions below. Give it to the controller through `serviceAccount.annotations` (IRSA) or a Pod Identity association of the ServiceAccount of the release.

The controller role of `aws-connectors-role` needs this policy. Replace the account, the region, the EKS cluster, the boundary, the role name prefix `camunda-connectors`, and the policies that you offer in `OfferedPolicies`. Remove the last two statements with IRSA.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "RolesWithTheBoundary",
      "Effect": "Allow",
      "Action": [
        "iam:CreateRole", "iam:PutRolePermissionsBoundary", "iam:PutRolePolicy",
        "iam:DeleteRolePolicy", "iam:DetachRolePolicy"
      ],
      "Resource": "arn:aws:iam::111122223333:role/camunda-connectors-*",
      "Condition": {
        "StringEquals": {"iam:PermissionsBoundary": "arn:aws:iam::111122223333:policy/connectors-boundary"}
      }
    },
    {
      "Sid": "OfferedPolicies",
      "Effect": "Allow",
      "Action": ["iam:AttachRolePolicy"],
      "Resource": "arn:aws:iam::111122223333:role/camunda-connectors-*",
      "Condition": {
        "StringEquals": {"iam:PermissionsBoundary": "arn:aws:iam::111122223333:policy/connectors-boundary"},
        "ArnLike": {"iam:PolicyARN": ["arn:aws:iam::111122223333:policy/connectors-*"]}
      }
    },
    {
      "Sid": "Roles",
      "Effect": "Allow",
      "Action": [
        "iam:GetRole", "iam:TagRole", "iam:UpdateAssumeRolePolicy", "iam:DeleteRole",
        "iam:GetRolePolicy", "iam:ListRolePolicies", "iam:ListAttachedRolePolicies"
      ],
      "Resource": "arn:aws:iam::111122223333:role/camunda-connectors-*"
    },
    {
      "Sid": "Health",
      "Effect": "Allow",
      "Action": ["iam:GetPolicy"],
      "Resource": "arn:aws:iam::111122223333:policy/connectors-boundary"
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
      "Resource": "arn:aws:iam::111122223333:role/camunda-connectors-*",
      "Condition": {"StringEquals": {"iam:PassedToService": "pods.eks.amazonaws.com"}}
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

The condition on the boundary makes sure that the controller cannot create a role, or give a role a policy, without the boundary. The condition on `iam:PolicyARN` limits the managed policies that the controller can attach. Without it, a controller that can attach a policy can attach `AdministratorAccess`.

With only `aws-connectors-existing-role`, the controller needs `sts:GetCallerIdentity`, and with Pod Identity also the statement `PodIdentityAssociations` and `iam:PassRole` on the role of the Connectors.

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `mode` | `irsa` | `irsa` or `podIdentity`. The chart refuses another value. |
| `roleNamePrefix` | `camunda-connectors` | The start of the name of each role of `aws-connectors-role`, at most 47 characters of `A-Z`, `a-z`, `0-9`, and `+=,.@_-`. It must differ from the role name prefix of aws-identity, `camunda` by default. Else both plugins write the same role for one claim. The controller refuses `camunda`. |
| `permissionsBoundaryArn` | `""` | Required for `aws-connectors-role`. The managed policy that bounds each role. The registration shows it as a limit. |
| `extraTags` | `{}` | Tags that go on each role and association, next to the tags of the request, for example `cost-center: platform`. A key cannot start with `aws:` or `camunda.io/`. |
| `oidcProviderArn` | `""` | Required with IRSA. The IAM OIDC provider of the EKS cluster. |
| `eksClusterName` | `""` | Required with Pod Identity. The EKS cluster of the associations. |
| `region` | `""` | The AWS region. Empty takes the region of the pod, such as `AWS_REGION`. With Pod Identity the controller needs a region, because the role names include it. |
| `serviceAccount.annotations` | `{}` | The annotations of the ServiceAccount of the controller, for example `eks.amazonaws.com/role-arn` of the controller role. |
| `registrations` | `aws-connectors-existing-role` and `aws-connectors-role` | The PluginRegistrations that the controller writes at startup and serves. Each item is a name and fields of a registration spec, merged over the defaults of the chart. Remove an item to offer only the other option. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/aws-connectors-role` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

The chart renders without the required values, but the controller does not start without them. Its log names the missing value.

An install with IRSA:

```sh
helm install aws-connectors-role charts/aws-connectors-role --namespace camunda-operator-plugins \
  --set permissionsBoundaryArn=arn:aws:iam::111122223333:policy/connectors-boundary \
  --set oidcProviderArn=arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE \
  --set serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn=arn:aws:iam::111122223333:role/aws-connectors-role-controller
```

## Parameters

`aws-connectors-existing-role` has one parameter:

| Parameter | Meaning |
| --- | --- |
| `roleArn` | Required. The IAM role of the Connectors, such as `arn:aws:iam::111122223333:role/connectors`. |

`aws-connectors-role` has three parameters:

| Parameter | Meaning |
| --- | --- |
| `managedPolicyArns` | Up to 10 managed policies that every cluster of the Environment gets. Set it in the Environment, not in a claim. |
| `extraPolicyArns` | Up to 10 managed policies that a claim can add. Make it claim-editable with an `allowed` list in the Environment. |
| `inlinePolicy` | One IAM policy document with placeholders, which scope it to one cluster. |

IAM allows 10 managed policies on a role by default. `managedPolicyArns` and `extraPolicyArns` together must stay under that quota.

An Environment that offers the role for each cluster, gives each cluster one policy, and lets a claim add SQS access:

```yaml
apiVersion: fleet.camunda.io/v1alpha1
kind: Environment
metadata:
  name: production
spec:
  sections:
    connectors-permissions:
      allowed: [aws-connectors-role]
      default: aws-connectors-role
  parameters:
    aws-connectors-role:
      managedPolicyArns:
        value: ["arn:aws:iam::111122223333:policy/connectors-base"]
      extraPolicyArns:
        claimEditable: true
        allowed:
          - name: sqs
            label: SQS access
            value: "arn:aws:iam::111122223333:policy/connectors-sqs"
```

The claim author ticks "SQS access", and the claim stores `sqs`, not the ARN. Without an `allowed` list, a claim-editable `extraPolicyArns` takes any policy ARN, so always give the list.

### Inline policy placeholders

pluginkit fills in the placeholders of `inlinePolicy` before the plugin sees it:

| Placeholder | Value |
| --- | --- |
| `{{ .namespace }}` | The namespace of the claim. |
| `{{ .claim }}` | The name of the claim. |
| `{{ .request }}` | The name of the request. |
| `{{ .serviceAccount }}` | `<claim>-connectors`. |
| `{{ .parameters.<name> }}` | The value of another parameter. |

`{{ .outputs }}` is not available. A placeholder works only in a string value, never in a JSON key such as a condition key. An unknown placeholder refuses the request with `InvalidParameters`.

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["sqs:SendMessage", "sqs:ReceiveMessage", "sqs:DeleteMessage"],
    "Resource": "arn:aws:sqs:eu-west-1:111122223333:{{ .namespace }}-{{ .claim }}-*"
  }]
}
```

IAM allows 10,240 characters, white space not counted, for the inline policies of a role. The plugin counts the filled-in policy the same way, and refuses it over that limit with `InvalidParameters`, before any AWS call. The message gives the size.

### Write a shared managed policy

A managed policy is the same for every cluster. To scope it to one cluster, use the tags of the role in a condition, for example the namespace:

```json
{
  "Effect": "Allow",
  "Action": "s3:GetObject",
  "Resource": "arn:aws:s3:::processes/${aws:PrincipalTag/camunda.io/namespace}/*"
}
```

## Outputs

| Key | Value | Read by |
| --- | --- | --- |
| `principal` | The ARN of the role, format `aws-iam-role-arn`. | other plugins |
| `serviceAccountAnnotations` | A JSON object. With IRSA `{"eks.amazonaws.com/role-arn": "<role ARN>"}`, with Pod Identity `{}`. | the cluster claim, which puts the annotations on the Connectors ServiceAccount |

With IRSA the annotation gives the role. With Pod Identity the association gives it. When a ServiceAccount has both, IRSA wins.

## Grants

The plugin writes no Grants and reads none.

## Deletion

The registrations offer the deletion policies `Delete` and `Orphan`.

| Registration | `Delete` | `Orphan` |
| --- | --- | --- |
| `aws-connectors-role` | Deletes the association (Pod Identity), the inline policies, the managed policy attachments, and the role. | Deletes the association (Pod Identity). Keeps the role and its policies. |
| `aws-connectors-existing-role` | Deletes the association (Pod Identity). Never deletes the role. | The same as `Delete`. |

The plugin deletes only an association that carries the tags of the claim. Two claims that share one existing role keep each other's association. An orphaned role keeps its tags, so you can find it by the namespace and the claim.

## Troubleshooting

| Reason | Condition | What to do |
| --- | --- | --- |
| `InvalidParameters` | `OutputsReady` and `Ready` of the request | The parameters do not match the schema, `inlinePolicy` has an unknown placeholder, or the filled-in `inlinePolicy` is over 10,240 characters. The message says which. Correct the Environment or the claim. |
| `MissingServiceAccount` | `OutputsReady` and `Ready` of the request | The request names no `spec.target.serviceAccountName`. A role that the plugin made for the request then trusts nobody, and with Pod Identity the associations of the claim go. The claim controller sets the ServiceAccount. Check the claim and the operator version. |
| `ReconcileError` | `Ready` of the request | An AWS call failed. The message gives the call and the AWS error, for example `LimitExceeded` when the role has more managed policies than its quota, or `AccessDenied` when the controller role lacks a permission above. pluginkit tries again. |
| `DeleteFailed` | `Ready` of a deleted request | The plugin could not delete the role or an association. The message gives the AWS error. The finalizer stays, and pluginkit tries again. |
| `Unhealthy` | `Ready` of the registration | The controller cannot call STS with its credentials, or it cannot read the permissions boundary. The message gives the error. Check the controller role and `permissionsBoundaryArn`. |

When the controller Pod does not get ready, read its log. The controller stops when it serves `aws-connectors-role` without `permissionsBoundaryArn`, with a `roleNamePrefix` that IAM refuses, or with the prefix `camunda`. It also stops when another required chart value is missing or not valid, when it cannot read the file of its registrations, or when the variable `POD_NAMESPACE` or `SERVICE_ACCOUNT_NAME` is not set.
