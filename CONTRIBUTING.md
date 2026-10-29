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

The workflow `.github/workflows/conformance.yml` has one job for each plugin, `Conformance of <plugin>`. The job creates a kind cluster, installs the operator from its chart, installs the chart of the plugin, and runs the conformance suite of the operator against one registration of the plugin. The operator is built from the commit that the api module of `go.mod` names.

The job runs on each push to `main` and on each pull request. On a pull request that changes only the docs, Markdown files, or the files of other plugins, the job stops after its first steps, and its check passes.

To add the job of a new plugin:

1. Add the name of the plugin, the directory name in `cmd/`, as one line to `matrix.plugin` in `.github/workflows/conformance.yml`.
2. Add the directory `test/conformance/<plugin>/` with these three files:

    | File | What it holds |
    | --- | --- |
    | `setup.sh` | An executable script that installs what the plugin needs before its chart, such as a Gateway API implementation or a storage server, and creates the targets of the requests. The scripts in `test/kind/` install the shared parts. |
    | `values.yaml` | The chart values of the run. The job sets the image of the plugin. |
    | `config.yaml` | The config of `plugin-conformance`: the registration, the namespace, the cases, and the timeouts. |

3. A maintainer then makes the check `Conformance of <plugin>` required for the `main` branch.

Copy `test/conformance/gateway-access/` or `test/conformance/rustfs/` as a start. `setup.sh` runs with `kubectl` and `helm` pointed at the kind cluster, from the root of the repository. It gets `CHART_NAMESPACE`, the namespace of the release, in its environment.

`make conformance-kind PLUGIN=<plugin>` runs the job on your machine, after `make operator-source`. It needs Docker, kind, kubebuilder, kubectl, and helm. It creates the kind cluster `camunda-operator-plugins` and leaves it after the run. Delete it with `kind delete cluster --name camunda-operator-plugins`.

### The AWS jobs

The AWS plugins run against a real AWS sandbox account, not against fakes. A job gets the credentials of a CI role from GitHub with OIDC. No access key exists. `hack/aws-sandbox/README.md` tells how the account is set up and what a job can do there.

GitHub gives no OIDC token to a pull request from a fork. As a result, the AWS jobs do not run on such a pull request. They run on `main` after the merge. A maintainer can push the branch to this repository to run them before the merge.

## 7. Before you open a pull request

1. Run `make lint`, `make test`, `make chart-verify`, and `make lint-renovate`. Each one must pass.
2. Write the commit subjects and the pull request title as `<type>(<area>): <imperative summary>`. The types are `feat`, `fix`, `refactor`, `test`, `chore`, and `docs`.

## 8. Releases

1. A maintainer publishes a GitHub release with a tag `vX.Y.Z`.
2. The release publishes the image and the chart of every plugin, all at version `X.Y.Z`.
3. The release then opens a pull request on `konsole-is/camunda-operator`. That pull request sets the versions in the catalog of official plugins that the fleet manager shows. It stops when a chart of the catalog has no release at the new version.
4. A prerelease, for example `v0.2.0-rc.1`, publishes the images and the charts, but it does not change the catalog.

The release opens the catalog pull request with a token of the GitHub App `camunda-operator-catalog`. To set up such an App, for example for a rebuilt repository:

1. Create a GitHub App in the `konsole-is` organization, with the repository permissions Contents: Read and write, and Pull requests: Read and write.
2. Install the App only on `konsole-is/camunda-operator`.
3. In this repository, set the variable `CATALOG_APP_ID` to the App ID.
4. In this repository, set the secret `CATALOG_APP_PRIVATE_KEY` to a private key of the App.
