# AWS sandbox scripts

The CI of the AWS plugins runs against a real AWS sandbox account. [`terraform/aws-sandbox`](../../terraform/aws-sandbox/README.md) sets up that account, and tells who gets its CI role and what a run can do there. This directory holds the scripts that a CI run uses.

## A run in CI

A job that uses AWS has the permission `id-token: write` and selects its environment as `.github/workflows/aws-sandbox.yml` does. It gets the credentials with `aws-actions/configure-aws-credentials` and the role `vars.AWS_ROLE_ARN`.

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

A cancelled job cannot remove its resources. The workflow `.github/workflows/aws-sandbox.yml` runs each hour. It removes each role, managed policy, OIDC provider, and bucket with the tag `cpo-ci-run` that is older than six hours. It schedules each such KMS key for deletion after 7 days, the shortest window, in the region of the sandbox and in the replica region of `AWS_KMS_REPLICA_REGION`. The issuer documents expire after one day.

To do a sweep from your machine, use administrator credentials:

```sh
AWS_REGION=eu-west-1 AWS_KMS_REPLICA_REGION=eu-central-1 hack/aws-sandbox/sweep.sh --older-than 6
```
