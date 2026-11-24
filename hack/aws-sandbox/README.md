# AWS sandbox scripts

The CI of the AWS plugins runs against a real AWS sandbox account. [`terraform/aws-sandbox`](../../terraform/aws-sandbox/README.md) sets up that account, and tells who gets its CI role and what a run can do there. This directory holds the scripts that a CI run uses.

## A run in CI

The job `conformance-aws` of `.github/workflows/conformance.yml` uses these scripts. It has one row for each set of `test/conformance/aws/` that runs in the sandbox: `single-release` and `multi-release`. `CONTRIBUTING.md` tells how a plugin gets such a row. A job that uses AWS has the permission `id-token: write` and selects its environment as `.github/workflows/aws-sandbox.yml` does. It gets the credentials with `aws-actions/configure-aws-credentials` and the role `vars.AWS_ROLE_ARN`.

The job `awskit-sandbox` of the same workflow runs no kind cluster. It uses only the credentials, the run ID, and `teardown.sh` of step 7. Steps 1 to 6 are for a run with a kind cluster.

Give each run an ID that starts with `cpo-ci-`, for example `cpo-ci-${{ github.run_id }}-${{ github.run_attempt }}`. Put the ID into the name of each resource of the run, and into its tag `cpo-ci-run`.

1. Publish the issuer of the kind cluster:

    ```sh
    hack/aws-sandbox/issuer.sh up "${RUN_ID}" "${RUNNER_TEMP}/issuer"
    ```

2. Start kind with the config that the script wrote:

    ```sh
    KIND_CONFIG="${RUNNER_TEMP}/issuer/kind.yaml" test/kind/up.sh
    ```

    The API server of this cluster signs the ServiceAccount tokens with the key of the run. STS trusts these tokens through the OIDC provider of the run.

3. Log in to ECR Public with the CI role. ECR Public limits an anonymous pull of the webhook image:

    ```sh
    aws ecr-public get-login-password --region us-east-1 | docker login --username AWS --password-stdin public.ecr.aws
    ```

4. Install the pod identity webhook, so that a ServiceAccount annotation gives a pod its role, as on EKS:

    ```sh
    test/kind/pod-identity-webhook.sh
    ```

5. Create the role of each release that calls AWS, with `plugin-role.sh <run ID> <name> <namespace> <service account> <policy file>...`. The role `<run ID>-<name>` trusts only the ServiceAccount. Each policy file becomes one managed policy on the role: `<run ID>-<name>` for one file, and `<run ID>-<name>-<x>` for each file `controller-policy-<x>.json` of more. For the release aws with aws-identity and aws-kms on:

    ```sh
    hack/aws-sandbox/plugin-role.sh "${RUN_ID}" aws camunda-operator-plugins aws \
      controller-policy-identity.json controller-policy-kms.json
    ```

6. Run the tests. A role of the run trusts the provider `oidc-provider/<bucket host>/<run ID>`, which `issuer.sh up` prints.
7. At the end of the job, also when the job fails, delete the kind cluster first, so that no controller creates an object again. Then remove what the run created:

    ```sh
    hack/aws-sandbox/teardown.sh "${RUN_ID}"
    ```

    `teardown.sh` runs `sweep.sh --run` and `issuer.sh down`. It fails when `leftovers.sh` still finds a role, a managed policy, an OIDC provider, or a bucket whose name has the run ID.

## An EKS run

The workflow `.github/workflows/eks-proof.yml` proves volume encryption and Pod Identity of the AWS plugins on a real EKS cluster. A maintainer starts it by hand: with `workflow_dispatch` on `main`, or by adding the label `eks-proof` to a pull request of this repository. Only the trusted user can start it, and only one EKS run runs at a time.

A run creates one EKS cluster with one `t3.xlarge` node, the volumes of one Camunda cluster, one KMS key, one hub role, and two images. `hack/aws-sandbox/eks-cluster.sh` creates the cluster, and `make journey-eks` runs the journey `eks` on it. Three layers remove what the run created:

- The last step of the job runs `eks-teardown.sh` when the job passes or fails. It fails when `leftovers.sh --eks` still finds anything of the run.
- AWS ends the node and the cluster also when the job is gone. A schedule terminates the node 150 minutes after the start of the run, and the node shuts itself down a few minutes before that. A second schedule deletes the cluster after 160 minutes. Each image expires one day after its push.
- The scheduled sweep removes each EKS run that is older than two hours. GitHub can skip scheduled runs, so the next sweep can come about 12 hours later. Until then, the volumes, the KMS key, and the hub role of a run can stay. They cost a few cents.

Do not cancel a run. On a cancel, GitHub gives the cleanup step only five minutes, which can be too short for the teardown. A step that hangs fails at its time limit, and the cleanup then gets its full 30 minutes. If a run was cancelled, remove it by hand at once.

To remove a run whose cleanup did not finish, use administrator credentials and the outputs of `terraform/aws-sandbox`. Run it again until it exits 0:

```sh
export AWS_REGION=eu-west-1 AWS_EKS_VPC_ID=$(terraform -chdir=terraform/aws-sandbox output -raw eks_vpc_id)
hack/aws-sandbox/eks-teardown.sh <run ID>
```

To check every region for a run, use administrator credentials:

```sh
hack/aws-sandbox/leftovers.sh --eks --all-regions <run ID>
```

## The sweep

A cancelled job cannot remove its resources. The workflow `.github/workflows/aws-sandbox.yml` is scheduled each hour, but GitHub skips scheduled runs when it is busy: runs came 5 to 12 hours apart. It removes each role, managed policy, OIDC provider, and bucket with the tag `cpo-ci-run` that is older than six hours. It schedules each such KMS key for deletion after 7 days, the shortest window, in the region of the sandbox and in the replica region of `AWS_KMS_REPLICA_REGION`. The issuer documents expire after one day.

To do a sweep from your machine, use administrator credentials:

```sh
AWS_REGION=eu-west-1 AWS_KMS_REPLICA_REGION=eu-central-1 hack/aws-sandbox/sweep.sh --older-than 6
```

The job `sweep-eks` of the same workflow removes each EKS cluster, node, volume, image, and schedule of an EKS run that is older than two hours. By hand, with administrator credentials:

```sh
AWS_REGION=eu-west-1 AWS_EKS_VPC_ID=<VPC ID> hack/aws-sandbox/eks-sweep.sh --older-than 2
```
