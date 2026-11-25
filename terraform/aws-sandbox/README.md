# AWS sandbox account

The CI of the AWS plugins runs against a real AWS account. A fake of IAM or STS cannot prove that a trust policy gives a pod its role. This Terraform configuration sets up that account and the GitHub environments of the AWS jobs. The scripts that a CI run uses, and the sweep, are in [`hack/aws-sandbox`](../../hack/aws-sandbox/README.md).

## What the setup creates

This configuration creates these resources:

| Resource | Name | Purpose |
| --- | --- | --- |
| IAM OIDC provider | `token.actions.githubusercontent.com` | GitHub Actions get AWS credentials without an access key. |
| IAM role | `cpo-sandbox-ci` | The role of the CI jobs. Its credentials last one hour at most. See [Who gets the role](#who-gets-the-role). |
| Managed policy | `cpo-sandbox-boundary` | The permissions boundary of the CI role and of each role that a run creates. |
| Managed policies | `cpo-sandbox-ci-iam`, `cpo-sandbox-ci-storage` | The permissions of the CI role, within the boundary. |
| S3 bucket | `cpo-sandbox-issuers-<account>-<region>` | The OIDC issuer documents of the kind cluster of each run. Objects expire after one day. |
| Budgets | `cpo-sandbox-monthly`, `cpo-sandbox-daily` | A monthly and a daily cost budget with email alerts, when you give alert addresses. |
| GitHub environments | `aws-sandbox`, `aws-sandbox-review` | The environments of the AWS jobs, with the variables `AWS_ROLE_ARN`, `AWS_REGION`, and `AWS_ISSUER_BUCKET`. |
| GitHub variable | `AWS_SANDBOX_ACTOR_ID` | The user ID of the trusted user. |
| VPC | `cpo-sandbox-eks` | The network of the EKS runs: two public subnets, an internet gateway, and no NAT gateway. |
| IAM role | `cpo-sandbox-eks` | The role of the EKS proof workflow. See [What an EKS run can do](#what-an-eks-run-can-do). |
| IAM roles | `cpo-sandbox-eks-cluster`, `cpo-sandbox-eks-node`, `cpo-sandbox-eks-ebs-csi`, `cpo-sandbox-eks-plugins`, `cpo-sandbox-eks-scheduler` | The roles of the control plane, the node, the EBS CSI controller, the release `aws`, and the schedules that delete a run. The node role has an instance profile of the same name. |
| Managed policy | `cpo-sandbox-eks-limits` | The cost limits of every role of an EKS run. |
| ECR repositories | `cpo-sandbox-eks/camunda-operator`, `cpo-sandbox-eks/aws` | The images of an EKS run. Images expire one day after their push. |
| Schedule group | `cpo-sandbox-eks` | The schedules that delete the cluster and the node of an EKS run. |
| GitHub environment | `aws-eks` | The environment of the EKS proof workflow, with the `AWS_EKS_*` variables. Each run waits for one approval of the trusted user. |
| GitHub variable | `AWS_EKS_SANDBOX` | `true` when the EKS parts exist. |
| Managed policy | `cpo-sandbox-sweep` | The list and delete rights of the sweeps, on resources of runs only. It allows no create, no tag write, and no pass of a role. |
| IAM role | `cpo-sandbox-eks-sweep` | The role of the scheduled EKS sweep in GitHub, with `cpo-sandbox-sweep` only. |
| GitHub environment | `aws-sandbox-sweep` | The environment of the scheduled EKS sweep: `main` only, no reviewer, with the variables `AWS_SWEEP_ROLE_ARN`, `AWS_REGION`, and `AWS_EKS_VPC_ID`. |
| Lambda function | `cpo-sandbox-sweep` | The hourly sweep in AWS, from `hack/aws-sandbox/sweeper`. Its role `cpo-sandbox-sweep` has `cpo-sandbox-sweep` and the right to write its log group `/aws/lambda/cpo-sandbox-sweep`, which keeps logs for 3 days. |
| Schedule | `cpo-sandbox-sweep` | Runs the function once an hour, in the schedule group `cpo-sandbox-sweep`, as the role `cpo-sandbox-sweep-scheduler`. |
| CloudWatch alarms | `cpo-sandbox-sweep-errors`, `cpo-sandbox-sweep-no-pass` | An email to `alert_emails` when a pass fails, or when no pass ran for two hours, through the SNS topic `cpo-sandbox-alerts`. |

The EKS parts cost nothing while no EKS run is active. The sweep in AWS stays within the free tier: one arm64 invocation of 128 MB an hour, no VPC, and two alarms.

No sweep removes a `cpo-sandbox-*` resource, because each one lists only `cpo-ci-*` names and resources with the tag `cpo-ci-run`.

The Terraform state is in the S3 bucket `cpo-sandbox-tfstate-<account>-<region>`, which you create by hand before the first apply.

## Who gets the role

The role trusts only the OIDC tokens of this repository, in one of two GitHub environments:

| Environment | Runs | Approval |
| --- | --- | --- |
| `aws-sandbox` | The runs of one trusted user: the scheduled sweep, pushes to `main`, and the pull requests of that user. | None. The role also checks the actor ID in the token, so a changed workflow cannot use this environment for another user. |
| `aws-sandbox-review` | Every other run, such as a pull request of another contributor or of Renovate. | A maintainer approves each run in GitHub. |

Three more environments have roles of their own:

| Environment | Runs | Approval |
| --- | --- | --- |
| `aws-eks` | The job `eks` of the EKS proof workflow, as the role `cpo-sandbox-eks`. | The trusted user approves each run in GitHub. |
| `aws-sandbox-sweep` | The job `sweep-eks` of the workflow `AWS sandbox`, on `main` only, as the role `cpo-sandbox-eks-sweep`. | None. |

A job selects the environment from `github.actor_id` and the repository variable `AWS_SANDBOX_ACTOR_ID`. GitHub gives no OIDC token to a pull request from a fork.

## What a run can do

The boundary allows IAM, STS, S3, and KMS, and a docker login to ECR Public. Each resource must have a name that starts with `cpo-ci-`. A role that a run creates gets only the policies that the run writes, so the boundary is the only limit on it. The boundary does not allow these actions:

- Create a role without the boundary or without the tag `cpo-ci-run`, or give that tag a value that does not start with `cpo-ci-`.
- Create a role or policy with a path.
- Attach a managed policy that does not start with `cpo-ci-`, such as `AdministratorAccess`.
- Remove the boundary from a role, or the tag `cpo-ci-run` from a resource.
- Create an IAM user, an access key, or a login profile.
- Change a `cpo-sandbox-*` role, policy, or bucket. This includes the Terraform state.
- Make a bucket public. A new bucket blocks public access, and the boundary does not allow a change of that block, of the ACLs, of the object ownership, or of the bucket policy.
- Use Object Lock on a bucket. A locked object stops the sweep.
- Write an object into the issuer bucket other than the two issuer documents of a run.
- Create a KMS key in a custom key store, or a key with a key policy other than the default one.
- Use a service outside the region of the sandbox. KMS also works in `kms_replica_region` (`eu-central-1` by default), so that a multi-Region key can have a replica there. The ECR Public login works in `us-east-1`, the only region that gives its token.
- Use any other service, such as EC2, EKS, or VPC.

A managed policy holds at most 6,144 characters, and a role has one boundary. So the boundary holds wide grants and the limits, and the CI role gets its exact grants from the other policies. Terraform stops before the apply when a policy is too long.

The cost of a run is a few cents. IAM and STS are free. A KMS key costs about 1 USD a month, counted by the hour, until it is scheduled for deletion.

## What an EKS run can do

The EKS proof workflow gets the role `cpo-sandbox-eks`. The role trusts only the OIDC tokens of the environment `aws-eks` from runs of the trusted user. Its credentials last two hours at most. The role has no boundary, because it cannot create an IAM role or policy: every role that the run uses is a role of this configuration.

The policy `cpo-sandbox-eks-limits` is on each role of an EKS run. With it, a role of a run cannot do these actions:

- Use a region other than the region of the sandbox.
- Start an instance of a type other than `eks_instance_types` (`t3.xlarge` or `t3a.xlarge`), or with an instance metadata hop limit above 1.
- Create a NAT gateway, an Elastic IP, a load balancer, a node group, an Auto Scaling group, a launch template, a snapshot, a log group, a VPC, or a CloudFormation stack.
- Create a volume other than a gp3 volume of at most 10 GiB at the free baseline of IOPS and throughput, with a tag `cpo-ci-run` that starts with `cpo-ci-`. This limit holds for the control plane and the EBS CSI controller, whose managed policies can create volumes. The root volume of a node has its own limit, below.
- Change a volume after its create.
- Remove the tag `cpo-ci-run`, or give it a value that does not start with `cpo-ci-`. The sweeps skip such a value.
- Write the tag `cpo-ci-run` on an EC2 resource after its create.

The run role can create a cluster, an instance, or a volume only with the tag `cpo-ci-run`. It can create only a cluster of standard support, without control plane logs. It can start a node only with default tenancy and a gp3 root volume of at most 30 GiB, at the free baseline of IOPS and throughput. It can create only the access entry of the node, and pass the two pod roles only to EKS Pod Identity.

The EBS CSI controller and the release `aws` get their credentials from EKS Pod Identity, whose session names the cluster of the run. With it, they write only the run tag of their own run. The release changes only the roles, keys, aliases, and Pod Identity associations of its own run.

IAM cannot limit what a trust policy holds, and a role of a run can change the trust of a role of a run. So the sweep removes each `cpo-ci-*` role, at any age, that trusts a principal that no run trusts: anyone, another account, an identity provider of GitHub or of another account, or a service other than EKS Pod Identity. `leftovers.sh` reports such a role.

A cluster and a node that every removal missed cost about 6.90 USD a day: 2.40 USD for the cluster, 4.38 USD for the `t3.xlarge` node, and 0.12 USD for its public IPv4 address. The daily budget sends an email when a day costs more than `daily_budget_limit_usd` (2 USD by default). It stops nothing, and AWS updates its cost data up to three times a day, typically 8 to 12 hours apart.

After an apply, check the limits with administrator credentials:

```sh
AWS_REGION=eu-west-1 hack/aws-sandbox/eks-limits.sh
```

## Apply the configuration

You need the Terraform of `.tool-versions`, the AWS CLI, administrator access to the account, and admin access to the repository.

1. Create the state bucket. Use versioning, block all public access, and add a bucket policy that refuses requests without TLS:

    ```sh
    aws s3api create-bucket --bucket cpo-sandbox-tfstate-<account>-<region> \
      --create-bucket-configuration LocationConstraint=<region>
    aws s3api put-bucket-versioning --bucket cpo-sandbox-tfstate-<account>-<region> \
      --versioning-configuration Status=Enabled
    ```

    For another account, set this bucket in the `backend "s3"` block of `versions.tf`.

2. Add the actor ID to the OIDC subject of the repository. The trust of the role reads it there. The GitHub provider of Terraform cannot set `use_immutable_subject`, so this step is manual:

    ```sh
    gh api -X PUT repos/konsole-is/camunda-operator-plugins/actions/oidc/customization/sub \
      --input - <<<'{"use_default": false, "include_claim_keys": ["repo", "context", "actor_id"], "use_immutable_subject": true}'
    ```

3. Apply the configuration:

    ```sh
    export GITHUB_TOKEN=$(gh auth token)
    terraform -chdir=terraform/aws-sandbox init
    terraform -chdir=terraform/aws-sandbox apply \
      -var 'alert_emails=["<address>"]'
    ```

    The apply also packs `hack/aws-sandbox/sweeper` into the function `cpo-sandbox-sweep`. Confirm the email of SNS that each address of `alert_emails` gets, or the alarms of the sweep send nothing.

    The defaults of `variables.tf` are those of this repository. If the account already has an OIDC provider for `token.actions.githubusercontent.com`, add `-var github_oidc_provider_arn=<its ARN>`. `alert_emails` is required: the EKS run role is not created without a budget alert. In an account of an AWS organization, the management account must first enable "Linked account access" in its Cost Management preferences.
