# Contributing

This guide gives the rules for a plugin in this repository. The maintainers and the code owners apply them in every review.

## 1. What a plugin is

1. A plugin has one directory in each of these places: `cmd/<plugin>/`, `internal/<plugin>/`, `charts/<plugin>/`, and `test/conformance/<plugin>/`. It also has one page, `docs/plugins/<plugin>.md`. In `internal/`, the name of the directory has no hyphens, because it is the name of a Go package.
2. To start a new plugin, copy the five parts of an existing plugin and rename them, for example `cmd/gateway-access/`, `internal/gatewayaccess/`, `charts/gateway-access/`, `test/conformance/gateway-access/`, and `docs/plugins/gateway-access.md`.
3. A plugin can run more than one controller, as `aws` does. Then `internal/<plugin>/` holds the shared manager, and each controller has its own package `internal/<plugin><controller>/`, for example `internal/awskms/`. The plugin has one docs page for the plugin, `docs/plugins/<plugin>.md`, and one for each controller, such as `docs/plugins/aws-kms.md`. Its chart has one values block for each controller, with the value `enabled`. Every controller is off by default.
4. Code that two or more plugins use goes in `pkg/`.
5. A plugin serves its requests through `pkg/pluginkit`. See [`docs/pluginkit.md`](docs/pluginkit.md).
6. All plugins are in one Go module. A release gives every plugin the version of the repository.

## 2. When a plugin is approved

A maintainer approves a plugin when all of these rules are true:

1. The plugin passes the conformance suite of the operator in CI, in its job of the Conformance workflow. See [section 6](#6-the-conformance-job).
2. The plugin has its own unit tests and its own envtest suite, and CI passes.
3. The plugin has a docs page in `docs/plugins/`.
4. The plugin imports only the published plugin API of the operator. That API is the module `github.com/konsole-is/camunda-operator/api`, and the conformance module in tests. `make lint` refuses an import of another package of the operator, and a `go.mod` that requires the root module of the operator.
5. The plugin has the Apache-2.0 license, the license of this repository.

## 3. Code owner

1. The contributor of a plugin becomes its code owner.
2. The pull request that adds the plugin adds one line to `.github/CODEOWNERS` for each of its directories and pages, as [section 1](#1-what-a-plugin-is) lists them.
3. The code owner reviews each change to the plugin.

## 4. Deprecation and removal

1. A plugin can fail the conformance suite or CI while its code owner does not answer for 90 days. Then the next release marks the plugin as deprecated.
2. The release after that removes the plugin.
3. Before the removal, a person can take over the plugin. That person fixes the plugin and becomes its code owner.

## 5. The chart rule

1. The chart of a plugin ships a ClusterRole with the verb `serve` on `pluginregistrations`. A chart with more than one controller ships one such ClusterRole for each controller that is on.
2. The `resourceNames` of that rule are the registrations of the controller, and no other names. A rule without `resourceNames` gives `serve` on every registration of the cluster, so the chart refuses to render a controller without a registration. It also refuses a registration name in two blocks that are on.
3. The chart binds each such ClusterRole to the ServiceAccount of the release.
4. The operator chart holds the one grant admission policy. Before it admits a `Grant`, the policy checks that the writer has `serve` on the registration of the `Grant`.
5. A plugin chart ships no `ValidatingAdmissionPolicy` and no `ValidatingAdmissionPolicyBinding`.
6. The chart binds only the ClusterRoles that it ships, and none of them has an `aggregationRule`. A role from outside the chart, such as `cluster-admin`, can give `serve` on every registration, and an aggregated role takes the rules of such roles.

7. The container `controller` runs with `-leader-elect`. The chart ships a Role and a RoleBinding that let the controller ServiceAccount take its Lease in the release namespace. A rolling update runs the old and the new Pod together, and only the leader writes.

`make chart-verify` checks these rules for the chart of each plugin. It renders the chart with each case of `hack/chartcheck/testdata/<plugin>/`. A case whose first line is `# refuses: <text>` must fail to render, with `<text>` in the error. Every other case must pass `helm lint` and the checks of the rules. Two releases of such a case must also share no object name and no leader election Lease. `make chart-verify` checks this for two releases in one namespace, and for two releases in two namespaces whose names can make the same text. Add a case for each way to turn the controllers on or off, and for each refusal of the chart.

## 6. The conformance job

The workflow `.github/workflows/conformance.yml` has one job for each plugin, or for each set of a plugin. A set is one way to install a plugin, with its own files in `test/conformance/<plugin>/<set>/`, and one row of a matrix. The job creates a kind cluster, installs the operator from its chart, installs the chart of the plugin, and runs the conformance suite of the operator against the registrations of the plugin. The operator is built from the commit that the api module of `go.mod` names.

The workflow has two matrices:

- The job `conformance`, with the check `Conformance of <check>`, is for a plugin or a set that needs only the kind cluster. It runs on each push to `main` and on each pull request. The row of the set `ebs-only` of the plugin `aws` keeps the check name `Conformance of aws-ebs`.
- The job `conformance-aws`, with the check `Conformance of aws (<set>) in the AWS sandbox`, is for a set that calls AWS. It runs against the AWS sandbox account. See [The AWS jobs](#the-aws-jobs).

On a pull request that changes only the docs, Markdown files, or the files of other plugins, a job that runs stops after its first steps, and its check passes.

To add the job of a new plugin:

1. Add one row `{check: <plugin>, plugin: <plugin>}` to `matrix.include` of the job `conformance` in `.github/workflows/conformance.yml`. The plugin is the directory name in `cmd/`, and the check is the name that the ruleset of `main` requires. For each set, add a row with `set: <set>` and a check name of its own. For a set that calls AWS, add the set to `matrix.set` of the job `conformance-aws` instead.
2. Add the directory `test/conformance/<plugin>/`, or one directory `test/conformance/<plugin>/<set>/` for each set, with these files:

    | File | What it holds |
    | --- | --- |
    | `setup.sh` | An executable script that installs what the plugin needs before its chart, such as a Gateway API implementation or a storage server, and creates the targets of the requests. The scripts in `test/kind/` install the shared parts. |
    | `values.yaml` | The chart values of the release `<plugin>`. The job sets the image of the plugin. |
    | `values-<release>.yaml` | Optional. The chart values of one more release `<release>` of the chart, in the same namespace. The job installs it after the release `<plugin>`. |
    | `config.yaml` | The config of `plugin-conformance`: the registration, the namespace, the cases, and the timeouts. |
    | `config-<name>.yaml` | Optional. One more run of the suite, for example against a second registration of the chart. Its report is `<plugin>-<name>.xml`. |
    | `check.sh` | Optional. An executable script that runs after the suites, for a promise of the plugin that the suite does not check. The job fails when it fails. |
    | `controller-policy-<controller>.json` | AWS plugins only, in `test/conformance/<plugin>/`. The IAM policy of one controller. Copy it from the docs page of the controller, so that a policy on the page that is too small fails the job. The role of the release gets one managed policy for each file. |

3. A maintainer then makes the check `Conformance of <plugin>` required for the `main` branch. An AWS check is never required.

Copy `test/conformance/gateway-access/` or `test/conformance/rustfs/` as a start. For an AWS plugin, copy `test/conformance/aws/`. `setup.sh` and `check.sh` run with `kubectl` and `helm` pointed at the kind cluster, from the root of the repository. They get `CHART_NAMESPACE`, the namespace of the release, in their environment.

Before the job uses `values.yaml` and each config, it replaces these variables of the run in them:

| Variable | Value |
| --- | --- |
| `${RUN_ID}` | The ID of the run, such as `cpo-ci-19234567890-1-0`. |
| `${AWS_ACCOUNT_ID}` | The sandbox account. |
| `${AWS_REGION}` | The region of the sandbox. |
| `${AWS_KMS_REPLICA_REGION}` | The second KMS region of the sandbox. |
| `${AWS_OIDC_PROVIDER_ARN}` | The IAM OIDC provider of the kind cluster of the run. |

The job fails on a file that names one of these variables when the variable has no value. A kind job sets none of them. Other text, such as `{name}` or `$5`, stays as it is. `setup.sh` fills the variables into its own files, such as `controller-policy-<controller>.json`, with `test/conformance/render.sh <file>`.

`setup.sh` can add a variable of its own, such as the ARN of an AWS object that it creates for the run. It appends the line `NAME=value` to the file that the environment variable `RUN_VARS` names. `check.sh` gets `RUN_VARS` too. The job then fills `${NAME}` in `values.yaml` and in each config, and `test/conformance/render.sh` fills it in the files of `check.sh`. A name holds only `A-Z`, `0-9`, and `_`, and starts with a letter. The file cannot set a variable of the table above, and it cannot set a name twice. For example, `test/conformance/aws/sandbox-setup.sh` creates a KMS key and writes `AWS_KMS_KEY_ARN`.

`make conformance-kind PLUGIN=<plugin>` runs the job of a kind plugin on your machine, and `CONFORMANCE_SET=<set>` picks a set, after `make operator-source`. It needs Docker, kind, kubebuilder, kubectl, and helm. It creates the kind cluster `camunda-operator-plugins` and leaves it after the run. Delete it with `kind delete cluster --name camunda-operator-plugins`. An AWS job runs only in CI.

### The AWS jobs

The AWS plugins run against a real AWS sandbox account. A job gets the credentials of a CI role from GitHub with OIDC. No access key exists. `terraform/aws-sandbox/README.md` tells how the account is set up and what a job can do there.

The AWS jobs run on each push to `main`, when a maintainer starts the workflow by hand, and on a pull request that the trusted user of the sandbox opened from a branch of this repository and pushed. The trusted user is the GitHub user whose ID is the repository variable `AWS_SANDBOX_ACTOR_ID`. Every other pull request skips them, and its AWS checks show as skipped: a pull request of another person, a pull request of the trusted user that another person pushed to, a pull request from a fork, and the pull requests of Renovate. A reopen of a pull request also skips them. Such a pull request gets no AWS credentials and waits for no approval in the environment `aws-sandbox-review`. Its AWS jobs run on `main` after the merge. A maintainer who read the change can run them before the merge on a branch of this repository, with `gh workflow run Conformance --ref <branch>`.

Each AWS job is one run with its own run ID, `cpo-ci-<GitHub run>-<attempt>-<row>`. The job:

1. Publishes an OIDC issuer for the run with `hack/aws-sandbox/issuer.sh`, and starts kind with it, so that STS trusts the ServiceAccount tokens of the cluster.
2. Installs cert-manager and `amazon-eks-pod-identity-webhook` with `test/kind/pod-identity-webhook.sh`. A pod under a ServiceAccount with the annotation `eks.amazonaws.com/role-arn` then gets the credentials of that role, as on EKS.
3. Creates the role of the release with `hack/aws-sandbox/plugin-role.sh`, with one managed policy for each `controller-policy-<controller>.json`. The chart gives the role to the ServiceAccount of the release through `serviceAccount.annotations`.
4. Runs the suites and `check.sh`.
5. Deletes the kind cluster, and runs `hack/aws-sandbox/teardown.sh`, also when an earlier step failed. The step fails when the run left a role, a managed policy, an OIDC provider, or a bucket.

The job `awskit-sandbox`, with the check `Fakes against the AWS sandbox`, tests the fakes of `pkg/awskit/awsfake` that the envtest suites of the AWS plugins use. The `*_cases_test.go` files of `pkg/awskit` hold shared cases: each case checks one behavior of an AWS API that awskit or a plugin relies on, such as an error code or the size of a page. To add a case, add it to the list of its service in those files, such as `iamCases` or `s3BucketCases`. `make test` runs the cases against the fakes. The job runs the same cases against the sandbox. It has the same triggers, credentials, and run ID rules as the other AWS jobs, but no kind cluster. Its run ID ends in `-awskit`. At the end it runs `hack/aws-sandbox/teardown.sh`, also when the cases failed. On a pull request, it runs only when the change touches `pkg/awskit/`, `go.mod`, `go.sum`, `hack/aws-sandbox/`, or the workflow. When the job fails because a fake answers otherwise than AWS, change the fake, not the case. A case that the sandbox cannot run, because the boundary refuses a call or the account lacks a service such as EKS, stays a test in `pkg/awskit/awsfake`. The head comment of `pkg/awskit/cases_test.go` lists these tests and tells why.

Each AWS object of a run has a name that starts with the run ID and the tag `cpo-ci-run` with the run ID as its value. Each role has the boundary `cpo-sandbox-boundary` and no path. So the values of an AWS plugin set a name prefix and the extra tag, for example `identity.roleNamePrefix: ${RUN_ID}-hub` and `extraTags: {cpo-ci-run: ${RUN_ID}}`. A KMS key gets a deletion window of 7 days, because the boundary refuses every other window.

A red run on `main` files the issue "Conformance failed on main", or adds a comment to it when it is open. Find the cause and fix it. Do not run the workflow again to make it green.

The Pod Identity mode of the AWS plugins needs EKS, so CI does not test it in the sandbox. Its envtest suites run with a fake of the EKS API.

## 7. The journeys

A journey proves that a set of plugins gives a working cluster from end to end. The workflow `.github/workflows/journeys.yml` runs the bare-metal journey on kind. A claim with `rustfs-bucket`, `gateway-public`, and the built-in Elasticsearch reaches `Ready`. Operate answers at the URL of the claim through the Gateway. A backup lands in the bucket, and a deletion of the claim with `Orphan` keeps the bucket.

The journey runs on each pull request that changes `rustfs`, `gateway-access`, `pkg/`, the journey, the kind scripts, or `go.mod`, and each night. The operator is built from the commit that the api module of `go.mod` names. The Camunda and Elasticsearch versions come from that commit too, from its file `test/e2e/matrix/<minor>.env`.

The workflow also runs the AWS journey in the AWS sandbox of [The AWS jobs](#the-aws-jobs), each night and on a manual run, never on a pull request. It runs only when the repository variable `AWS_SANDBOX_ACTOR_ID` is set. A claim with `aws-irsa`, `aws-kms-key`, `aws-s3`, and the built-in Elasticsearch gets a role, a key, and a bucket. The bucket holds the key and trusts the role. The claim reaches `Ready`, and a backup with Elasticsearch lands in the bucket, encrypted with the key of the claim. A deletion of the claim with `Delete` removes the role and the bucket with the backup in it, and schedules the deletion of the key. The journey installs one release `aws` of the chart `aws`, with aws-identity, aws-kms, and aws-s3 on, from `test/e2e/aws/values.yaml`. It takes the controller policies of these three from `test/conformance/aws/controller-policy-<controller>.json`. It does not run a `setup.sh` of `test/conformance/`, so these files can name the variables of the run, but not a variable of `RUN_VARS`. It reads AWS from a pod, as the role `<run>-journey-reader` with the policy `test/e2e/aws/reader-policy.json`. Its job removes what the run created, also when the run fails or is cancelled.

Each journey runs once for each supported Camunda minor, with the check `Bare-metal journey on Camunda <minor>` or `AWS journey on Camunda <minor>`. `CAMUNDA_MINORS` in the `Makefile` lists the minors, and the workflow reads it with `make list-camunda-minors`. To add a minor:

1. Make sure that the operator has the file `test/e2e/matrix/<minor>.env`, and that `go.mod` names an operator commit that holds it. The journey of a minor without that file fails before it creates the cluster.
2. Add the minor to `CAMUNDA_MINORS`, with the oldest minor first.
3. Correct what the journey needs for the new minor in the same pull request.

A journey is a directory `test/e2e/<journey>/` with an executable `setup.sh`, and one `Describe` with the label `<journey>` in `test/e2e/`. `setup.sh` installs what the journey needs beside the operator and ECK. A new journey gets its own job in `.github/workflows/journeys.yml`, with `needs: minors` and the matrix of the job `bare-metal`, so that it runs on the same minors. `test/e2e/run.sh` gives the specs of each journey a time limit that leaves the job time for its cleanup and its logs. A journey whose job has another `timeout-minutes` gets its own limit there. `make journey-kind JOURNEY=<journey>` runs a journey after `make operator-source`, with the tools of [section 6](#6-the-conformance-job). It runs on the first minor of `CAMUNDA_MINORS`, or on `CAMUNDA_MINOR=<minor>`. It creates the kind cluster `camunda-operator-plugins` and leaves it after the run.

A red run of the night files the issue "Journeys failed on main", or adds a comment to it when it is open. A red journey is a bug to fix, not a run to repeat.

## 8. Before you open a pull request

1. Run `make lint`, `make test`, `make chart-verify`, and `make lint-renovate`. Each one must pass.
2. Write the commit subjects and the pull request title as `<type>(<area>): <imperative summary>`. The types are `feat`, `fix`, `refactor`, `test`, `chore`, and `docs`.

## 9. Releases

1. A maintainer publishes a GitHub release with a tag `vX.Y.Z`.
2. The release publishes the image and the chart of every plugin, all at version `X.Y.Z`.
3. The release then opens a pull request on `konsole-is/camunda-operator`. That pull request sets the versions in the catalog of official plugins that the fleet manager shows. It stops when a chart of the catalog has no release at the new version.
4. A prerelease, for example `v0.2.0-rc.1`, publishes the images and the charts, but it does not change the catalog.
5. Until the catalog of the operator lists the chart `aws` in place of the five AWS charts, the catalog step of a full release stops. Until then, cut a prerelease, or run the workflow Catalog by hand after the catalog lists `aws`.

The release opens the catalog pull request with a token of the GitHub App `camunda-operator-catalog`. To set up such an App, for example for a rebuilt repository:

1. Create a GitHub App in the `konsole-is` organization, with the repository permissions Contents: Read and write, and Pull requests: Read and write.
2. Install the App only on `konsole-is/camunda-operator`.
3. In this repository, set the variable `CATALOG_APP_ID` to the App ID.
4. In this repository, set the secret `CATALOG_APP_PRIVATE_KEY` to a private key of the App.
