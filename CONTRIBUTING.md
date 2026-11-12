# Contributing

This guide gives the rules for a plugin in this repository. The maintainers and the code owners apply them in every review.

## 1. What a plugin is

1. A plugin has one directory in each of these places: `cmd/<plugin>/`, `internal/<plugin>/`, `charts/<plugin>/`, and `test/conformance/<plugin>/`. It also has one page, `docs/plugins/<plugin>.md`.
2. To start a new plugin, copy the five parts of an existing plugin and rename them, for example `cmd/gateway-access/`, `internal/gatewayaccess/`, `charts/gateway-access/`, `test/conformance/gateway-access/`, and `docs/plugins/gateway-access.md`.
3. Code that two or more plugins use goes in `pkg/`.
4. A plugin serves its requests through `pkg/pluginkit`. See [`docs/pluginkit.md`](docs/pluginkit.md).
5. All plugins are in one Go module. A release gives every plugin the version of the repository.

## 2. When a plugin is approved

A maintainer approves a plugin when all of these rules are true:

1. The plugin passes the conformance suite of the operator in CI, in its job of the Conformance workflow. See [section 6](#6-the-conformance-job).
2. The plugin has its own unit tests and its own envtest suite, and CI passes.
3. The plugin has a docs page in `docs/plugins/`.
4. The plugin imports only the published plugin API of the operator. That API is the module `github.com/konsole-is/camunda-operator/api`, and the conformance module in tests. `make lint` refuses an import of another package of the operator, and a `go.mod` that requires the root module of the operator.
5. The plugin has the Apache-2.0 license, the license of this repository.

## 3. Code owner

1. The contributor of a plugin becomes its code owner.
2. The pull request that adds the plugin adds one line to `.github/CODEOWNERS` for each of its five paths.
3. The code owner reviews each change to the plugin.

## 4. Deprecation and removal

1. A plugin can fail the conformance suite or CI while its code owner does not answer for 90 days. Then the next release marks the plugin as deprecated.
2. The release after that removes the plugin.
3. Before the removal, a person can take over the plugin. That person fixes the plugin and becomes its code owner.

## 5. The chart rule

1. The chart of a plugin ships a ClusterRole with the verb `serve` on `pluginregistrations`.
2. The `resourceNames` of that rule are the registrations of the chart, and no other names. A rule without `resourceNames` gives `serve` on every registration of the cluster, so the chart refuses to render without a registration.
3. The chart binds that ClusterRole to the ServiceAccount of the controller.
4. The operator chart holds the one grant admission policy. Before it admits a `Grant`, the policy checks that the writer has `serve` on the registration of the `Grant`.
5. A plugin chart ships no `ValidatingAdmissionPolicy` and no `ValidatingAdmissionPolicyBinding`.
6. The chart binds only the ClusterRoles that it ships, and none of them has an `aggregationRule`. A role from outside the chart, such as `cluster-admin`, can give `serve` on every registration, and an aggregated role takes the rules of such roles.

7. The container `controller` runs with `-leader-elect`. The chart ships a Role and a RoleBinding that let the controller ServiceAccount take its Lease in the release namespace. A rolling update runs the old and the new Pod together, and only the leader writes.

`make chart-verify` checks these rules for the chart of each plugin.

## 6. The conformance job

The workflow `.github/workflows/conformance.yml` has one job for each plugin. The job creates a kind cluster, installs the operator from its chart, installs the chart of the plugin, and runs the conformance suite of the operator against the registrations of the plugin. The operator is built from the commit that the api module of `go.mod` names.

The workflow has two matrices:

- The job `conformance`, with the check `Conformance of <plugin>`, is for a plugin that needs only the kind cluster. It runs on each push to `main` and on each pull request.
- The job `conformance-aws`, with the check `Conformance of <plugin> in the AWS sandbox`, is for a plugin that calls AWS. It runs against the AWS sandbox account. See [The AWS jobs](#the-aws-jobs).

On a pull request that changes only the docs, Markdown files, or the files of other plugins, a job that runs stops after its first steps, and its check passes.

To add the job of a new plugin:

1. Add the name of the plugin, the directory name in `cmd/`, as one line to `matrix.plugin` of the job `conformance` in `.github/workflows/conformance.yml`. For a plugin that calls AWS, add it to `matrix.plugin` of the job `conformance-aws` instead.
2. Add the directory `test/conformance/<plugin>/` with these files:

    | File | What it holds |
    | --- | --- |
    | `setup.sh` | An executable script that installs what the plugin needs before its chart, such as a Gateway API implementation or a storage server, and creates the targets of the requests. The scripts in `test/kind/` install the shared parts. |
    | `values.yaml` | The chart values of the run. The job sets the image of the plugin. |
    | `config.yaml` | The config of `plugin-conformance`: the registration, the namespace, the cases, and the timeouts. |
    | `config-<name>.yaml` | Optional. One more run of the suite, for example against a second registration of the chart. Its report is `<plugin>-<name>.xml`. |
    | `check.sh` | Optional. An executable script that runs after the suites, for a promise of the plugin that the suite does not check. The job fails when it fails. |
    | `controller-policy.json` | AWS plugins only. The IAM policy of the role of the controller. Copy it from the docs page of the plugin, so that a policy on the page that is too small fails the job. |

3. A maintainer then makes the check `Conformance of <plugin>` required for the `main` branch. An AWS check is never required.

Copy `test/conformance/gateway-access/` or `test/conformance/rustfs/` as a start, or `test/conformance/aws-kms/` for an AWS plugin. `setup.sh` and `check.sh` run with `kubectl` and `helm` pointed at the kind cluster, from the root of the repository. They get `CHART_NAMESPACE`, the namespace of the release, in their environment.

Before the job uses `values.yaml` and each config, it replaces these variables of the run in them:

| Variable | Value |
| --- | --- |
| `${RUN_ID}` | The ID of the run, such as `cpo-ci-19234567890-1-0`. |
| `${AWS_ACCOUNT_ID}` | The sandbox account. |
| `${AWS_REGION}` | The region of the sandbox. |
| `${AWS_KMS_REPLICA_REGION}` | The second KMS region of the sandbox. |
| `${AWS_OIDC_PROVIDER_ARN}` | The IAM OIDC provider of the kind cluster of the run. |

The job fails on a file that names one of these variables when the variable has no value. A kind job sets none of them. Other text, such as `{name}` or `$5`, stays as it is. `setup.sh` fills the variables into its own files, such as `controller-policy.json`, with `test/conformance/render.sh <file>`.

`make conformance-kind PLUGIN=<plugin>` runs the job of a kind plugin on your machine, after `make operator-source`. It needs Docker, kind, kubebuilder, kubectl, and helm. It creates the kind cluster `camunda-operator-plugins` and leaves it after the run. Delete it with `kind delete cluster --name camunda-operator-plugins`. An AWS job runs only in CI.

### The AWS jobs

The AWS plugins run against a real AWS sandbox account, not against fakes. A job gets the credentials of a CI role from GitHub with OIDC. No access key exists. `terraform/aws-sandbox/README.md` tells how the account is set up and what a job can do there.

The AWS jobs run on each push to `main`, when a maintainer starts the workflow by hand, and on a pull request that the trusted user of the sandbox opened from a branch of this repository and pushed. The trusted user is the GitHub user whose ID is the repository variable `AWS_SANDBOX_ACTOR_ID`. Every other pull request skips them, and its AWS checks show as skipped: a pull request of another person, a pull request of the trusted user that another person pushed to, a pull request from a fork, and the pull requests of Renovate. A reopen of a pull request also skips them. Such a pull request gets no AWS credentials and waits for no approval in the environment `aws-sandbox-review`. Its AWS jobs run on `main` after the merge. A maintainer who read the change can run them before the merge on a branch of this repository, with `gh workflow run Conformance --ref <branch>`.

Each AWS job is one run with its own run ID, `cpo-ci-<GitHub run>-<attempt>-<row>`. The job:

1. Publishes an OIDC issuer for the run with `hack/aws-sandbox/issuer.sh`, and starts kind with it, so that STS trusts the ServiceAccount tokens of the cluster.
2. Installs cert-manager and `amazon-eks-pod-identity-webhook` with `test/kind/pod-identity-webhook.sh`. A pod under a ServiceAccount with the annotation `eks.amazonaws.com/role-arn` then gets the credentials of that role, as on EKS.
3. Creates the role of the controller with `hack/aws-sandbox/plugin-role.sh`, from `controller-policy.json`. The chart gives the role to the controller through `serviceAccount.annotations`.
4. Runs the suites and `check.sh`.
5. Deletes the kind cluster, and runs `hack/aws-sandbox/teardown.sh`, also when an earlier step failed. The step fails when the run left a role, a managed policy, an OIDC provider, or a bucket.

Each AWS object of a run has a name that starts with the run ID and the tag `cpo-ci-run` with the run ID as its value. Each role has the boundary `cpo-sandbox-boundary` and no path. So the values of an AWS plugin set a name prefix and the extra tag, for example `roleNamePrefix: ${RUN_ID}-hub` and `extraTags: {cpo-ci-run: ${RUN_ID}}`. A KMS key gets a deletion window of 7 days, because the boundary refuses every other window.

A red run on `main` files the issue "Conformance failed on main", or adds a comment to it when it is open. Find the cause and fix it. Do not run the workflow again to make it green.

The Pod Identity mode of the AWS plugins needs EKS, so CI does not test it in the sandbox. Its envtest suites run with a fake of the EKS API.

## 7. The journeys

A journey proves that a set of plugins gives a working cluster from end to end. The workflow `.github/workflows/journeys.yml` runs the bare-metal journey on kind. A claim with `rustfs-bucket`, `gateway-public`, and the built-in Elasticsearch reaches `Ready`. Operate answers at the URL of the claim through the Gateway. A backup lands in the bucket, and a deletion of the claim with `Orphan` keeps the bucket.

The job runs on each pull request that changes `rustfs`, `gateway-access`, `pkg/`, the journey, the kind scripts, or `go.mod`, and each night. The operator is built from the commit that the api module of `go.mod` names. The Camunda and Elasticsearch versions come from that commit too.

A journey is a directory `test/e2e/<journey>/` with an executable `setup.sh`, and one `Describe` with the label `<journey>` in `test/e2e/`. `setup.sh` installs what the journey needs beside the operator and ECK. `make journey-kind JOURNEY=<journey>` runs a journey after `make operator-source`, with the tools of [section 6](#6-the-conformance-job). It creates the kind cluster `camunda-operator-plugins` and leaves it after the run.

A red journey is a bug to fix, not a run to repeat.

## 8. Before you open a pull request

1. Run `make lint`, `make test`, `make chart-verify`, and `make lint-renovate`. Each one must pass.
2. Write the commit subjects and the pull request title as `<type>(<area>): <imperative summary>`. The types are `feat`, `fix`, `refactor`, `test`, `chore`, and `docs`.

## 9. Releases

1. A maintainer publishes a GitHub release with a tag `vX.Y.Z`.
2. The release publishes the image and the chart of every plugin, all at version `X.Y.Z`.
3. The release then opens a pull request on `konsole-is/camunda-operator`. That pull request sets the versions in the catalog of official plugins that the fleet manager shows. It stops when a chart of the catalog has no release at the new version.
4. A prerelease, for example `v0.2.0-rc.1`, publishes the images and the charts, but it does not change the catalog.

The release opens the catalog pull request with a token of the GitHub App `camunda-operator-catalog`. To set up such an App, for example for a rebuilt repository:

1. Create a GitHub App in the `konsole-is` organization, with the repository permissions Contents: Read and write, and Pull requests: Read and write.
2. Install the App only on `konsole-is/camunda-operator`.
3. In this repository, set the variable `CATALOG_APP_ID` to the App ID.
4. In this repository, set the secret `CATALOG_APP_PRIVATE_KEY` to a private key of the App.
