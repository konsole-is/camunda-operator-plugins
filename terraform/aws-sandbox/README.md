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
| Budget | `cpo-sandbox-monthly` | A monthly cost budget with email alerts, when you give alert addresses. |
| GitHub environments | `aws-sandbox`, `aws-sandbox-review` | The environments of the AWS jobs, with the variables `AWS_ROLE_ARN`, `AWS_REGION`, and `AWS_ISSUER_BUCKET`. |
| GitHub variable | `AWS_SANDBOX_ACTOR_ID` | The user ID of the trusted user. |

The Terraform state is in the S3 bucket `cpo-sandbox-tfstate-<account>-<region>`, which you create by hand before the first apply.

## Who gets the role

The role trusts only the OIDC tokens of this repository, in one of two GitHub environments:

| Environment | Runs | Approval |
| --- | --- | --- |
| `aws-sandbox` | The runs of one trusted user: the scheduled sweep, pushes to `main`, and the pull requests of that user. | None. The role also checks the actor ID in the token, so a changed workflow cannot use this environment for another user. |
| `aws-sandbox-review` | Every other run, such as a pull request of another contributor or of Renovate. | A maintainer approves each run in GitHub. |

A job selects the environment from `github.actor_id` and the repository variable `AWS_SANDBOX_ACTOR_ID`. GitHub gives no OIDC token to a pull request from a fork.

## What a run can do

The boundary allows IAM, STS, S3, and KMS only. Each resource must have a name that starts with `cpo-ci-`. A role that a run creates gets only the policies that the run writes, so the boundary is the only limit on it. The boundary does not allow these actions:

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
- Use a service outside the region of the sandbox. KMS also works in `kms_replica_region` (`eu-central-1` by default), so that a multi-Region key can have a replica there.
- Use any other service, such as EC2, EKS, or VPC.

A managed policy holds at most 6,144 characters, and a role has one boundary. So the boundary holds wide grants and the limits, and the CI role gets its exact grants from the other policies. Terraform stops before the apply when a policy is too long.

The cost of a run is a few cents. IAM and STS are free. A KMS key costs about 1 USD a month, counted by the hour, until it is scheduled for deletion.

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

    The defaults of `variables.tf` are those of this repository. If the account already has an OIDC provider for `token.actions.githubusercontent.com`, add `-var github_oidc_provider_arn=<its ARN>`. Without `alert_emails`, Terraform creates no budget. In an account of an AWS organization, the management account must first enable "Linked account access" in its Cost Management preferences.
