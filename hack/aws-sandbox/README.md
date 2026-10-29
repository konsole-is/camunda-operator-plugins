# AWS sandbox account

The CI of the AWS plugins runs against a real AWS account. A fake of IAM or STS cannot prove that a trust policy gives a pod its role. This directory holds the setup of that account and the scripts that the CI jobs use.

## What the setup creates

`template.yaml` is a CloudFormation template. It creates these resources:

| Resource | Name | Purpose |
| --- | --- | --- |
| IAM OIDC provider | `token.actions.githubusercontent.com` | GitHub Actions get AWS credentials without an access key. |
| IAM role | `cpo-sandbox-ci` | The role of the CI jobs. It trusts only the OIDC tokens of this repository: `main`, its pull requests, and the environment `aws-sandbox`. |
| Managed policy | `cpo-sandbox-boundary` | The permissions of the CI role, and the permissions boundary of the CI role and of each role that a run creates. |
| S3 bucket | `cpo-sandbox-issuers-<account>-<region>` | The OIDC issuer documents of the kind cluster of each run. Objects expire after one day. |
| Budget | `cpo-sandbox-monthly` | An optional monthly cost budget with email alerts. |

## What a run can do

The boundary allows IAM, STS, S3, and KMS only. Each resource must have a name that starts with `cpo-ci-`. Most actions also need the tag `cpo-ci-run` on the resource. The boundary does not allow these actions:

- Create a role without the boundary or without the tag `cpo-ci-run`.
- Attach a managed policy that does not start with `cpo-ci-`, such as `AdministratorAccess`.
- Remove the boundary from a role.
- Create an IAM user, an access key, or a login profile.
- Change the CI role, the boundary, or the issuer bucket.
- Make a bucket public with an ACL.
- Create a KMS key in a custom key store, or a multi-Region key.
- Use any other service, such as EC2, EKS, or VPC.

The cost of a run is a few cents. IAM and STS are free. A KMS key costs about 1 USD a month, counted by the hour, until it is scheduled for deletion.

## Apply the template

You need the AWS CLI and administrator access to the account.

1. Create the stack:

    ```sh
    aws cloudformation deploy \
      --stack-name cpo-sandbox \
      --template-file hack/aws-sandbox/template.yaml \
      --capabilities CAPABILITY_NAMED_IAM \
      --region eu-west-1
    ```

2. If the account already has an OIDC provider for `token.actions.githubusercontent.com`, add `--parameter-overrides GitHubOidcProviderArn=<its ARN>`.
3. To get cost alerts, add `BudgetEnabled=true AlertEmail=<address>` to the parameter overrides. You can also add `SecondAlertEmail=<address>`. In an account of an AWS organization, the management account must first enable "Linked account access" in its Cost Management preferences.
4. Get the outputs of the stack:

    ```sh
    aws cloudformation describe-stacks --stack-name cpo-sandbox --region eu-west-1 \
      --query 'Stacks[0].Outputs'
    ```

5. In the repository settings, create the environment `aws-sandbox`. Give it these variables from the outputs:

    | Variable | Output |
    | --- | --- |
    | `AWS_ROLE_ARN` | `CiRoleArn` |
    | `AWS_REGION` | `Region` |
    | `AWS_ISSUER_BUCKET` | `IssuerBucket` |

## A run in CI

A job that uses AWS has the environment `aws-sandbox` and the permission `id-token: write`. It gets the credentials with `aws-actions/configure-aws-credentials` and the role `vars.AWS_ROLE_ARN`.

Give each run an ID that starts with `cpo-ci-`, for example `cpo-ci-${{ github.run_id }}-${{ github.run_attempt }}`. Put the ID into the name of each resource of the run, and into its tag `cpo-ci-run`.

1. Publish the issuer of the kind cluster:

    ```sh
    hack/aws-sandbox/issuer.sh up "${RUN_ID}" "${RUNNER_TEMP}/issuer"
    ```

2. Start kind with the config that the script wrote:

    ```sh
    kind create cluster --config "${RUNNER_TEMP}/issuer/kind.yaml"
    ```

    The API server of this cluster signs the ServiceAccount tokens with the key of the run. STS trusts these tokens through the OIDC provider of the run.

3. Run the tests. A role of the run trusts the provider `oidc-provider/<bucket host>/<run ID>`, which the script prints.
4. At the end of the job, also when the job fails, remove what the run created:

    ```sh
    hack/aws-sandbox/sweep.sh --run "${RUN_ID}"
    hack/aws-sandbox/issuer.sh down "${RUN_ID}"
    ```

## The sweep

A cancelled job cannot remove its resources. The workflow `.github/workflows/aws-sandbox.yml` runs each hour. It removes each role, managed policy, OIDC provider, and bucket with the tag `cpo-ci-run` that is older than six hours. It schedules each such KMS key for deletion after 7 days, the shortest window. The issuer documents expire after one day.

To do a sweep from your machine, use administrator credentials:

```sh
AWS_REGION=eu-west-1 hack/aws-sandbox/sweep.sh --older-than 6
```
