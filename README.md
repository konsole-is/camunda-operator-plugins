# camunda-operator-plugins

This repository holds the official plugins of the [Camunda operator](https://github.com/konsole-is/camunda-operator). A plugin brings options for a section of a cluster claim, for example a backup bucket or a cloud identity. Each plugin is one controller with its own image and its own Helm chart.

## Install a plugin

Each chart is in the GitHub Container Registry. To install a plugin, run this command with the name of the plugin and the version of a release.

```sh
helm install <plugin> oci://ghcr.io/konsole-is/camunda-operator-plugins/charts/<plugin> \
  --version <version> --namespace camunda-operator-plugins --create-namespace
```

The fleet manager of the operator lists the official charts with the command for each one. Every plugin of a release has the same version.

The page of each plugin in [`docs/`](docs/index.md) gives its prerequisites and its chart values.

## Layout

```
go.mod              github.com/konsole-is/camunda-operator-plugins
pkg/pluginkit/      the shared library of every plugin
pkg/                code that two or more plugins use
cmd/<plugin>/       the main package of each plugin
internal/<plugin>/  the controller of each plugin
charts/<plugin>/    the chart of each plugin
test/e2e/           the journeys, behind the e2e build tag
docs/               one page for each plugin, and the pluginkit page
hack/               the tools of the Makefile
```

The `gateway-access` plugin serves the access section: it gives a cluster or a management plane host names, and routes them through a Gateway API Gateway. The `rustfs` plugin serves the backup storage section: it gives each cluster its own RustFS bucket and access key. [`docs/pluginkit.md`](docs/pluginkit.md) describes the library for plugin authors.

## Development

`.tool-versions` gives the versions of Go, Helm, and cosign. `make lint-renovate` also needs `npx`, and the image targets need Docker.

| Command | What it does |
| --- | --- |
| `make test` | Runs the unit tests and the envtest suites. It downloads the envtest binaries and the CRD bases of the operator commit that `go.mod` names. |
| `make lint` | Runs golangci-lint. |
| `make chart-verify` | Lints and renders the chart of each plugin, and checks the chart rule. |
| `make build` | Builds the binary of each plugin into `bin/`. |
| `make docker-build PLUGIN=<plugin>` | Builds the image of one plugin. |
| `make help` | Lists every target. |

## Contributing

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) before you add a plugin. It gives the rules for approval, code owners, deprecation, and charts.

## License

Apache-2.0. See [`LICENSE`](LICENSE).
