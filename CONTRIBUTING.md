# Contributing

This guide gives the rules for a plugin in this repository. The maintainers and the code owners apply them in every review.

## 1. What a plugin is

1. A plugin has one directory in each of these places: `cmd/<plugin>/`, `internal/<plugin>/`, and `charts/<plugin>/`. It also has one page, `docs/plugins/<plugin>.md`.
2. To start a new plugin, copy the four parts of the skeleton plugin and rename them: `cmd/skeleton/`, `internal/skeleton/`, `charts/skeleton/`, and `docs/plugins/skeleton.md`.
3. Code that two or more plugins use goes in `pkg/`.
4. All plugins are in one Go module. A release gives every plugin the version of the repository.

## 2. When a plugin is approved

A maintainer approves a plugin when all of these rules are true:

1. The plugin passes the conformance suite of the operator in CI.
2. The plugin has its own unit tests and its own envtest suite, and CI passes.
3. The plugin has a docs page in `docs/plugins/`.
4. The plugin imports only the published plugin API of the operator. That API is the module `github.com/konsole-is/camunda-operator/api`, and the conformance module in tests. `make lint` refuses an import of another package of the operator, and a `go.mod` that requires the root module of the operator.
5. The plugin has the Apache-2.0 license, the license of this repository.

## 3. Code owner

1. The contributor of a plugin becomes its code owner.
2. The pull request that adds the plugin adds one line to `.github/CODEOWNERS` for each of its four paths.
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

`make chart-verify` checks these rules for the chart of each plugin.

## 6. Before you open a pull request

1. Run `make lint`, `make test`, `make chart-verify`, and `make lint-renovate`. Each one must pass.
2. Write the commit subjects and the pull request title as `<type>(<area>): <imperative summary>`. The types are `feat`, `fix`, `refactor`, `test`, `chore`, and `docs`.

## 7. Releases

1. A maintainer publishes a GitHub release with a tag `vX.Y.Z`.
2. The release publishes the image and the chart of every plugin, all at version `X.Y.Z`.
3. The release then opens a pull request on `konsole-is/camunda-operator`. That pull request sets the versions in the catalog of official plugins that the fleet manager shows. It stops when a chart of the catalog has no release at the new version.
4. A prerelease, for example `v0.2.0-rc.1`, publishes the images and the charts, but it does not change the catalog.
