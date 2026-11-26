# camunda-operator-plugins

This repository holds the official plugins of the [Camunda operator](https://github.com/konsole-is/camunda-operator). A plugin brings options for a section of a cluster claim, for example a backup bucket or a cloud identity. Each plugin has its own image and its own Helm chart. The `aws` plugin runs five controllers in one Pod.

## Install a plugin

Each chart is in the GitHub Container Registry. To install a plugin, run this command with the name of the plugin, `gateway-access`, `rustfs`, or `aws`, the version of a release, and a file of chart values.

```sh
helm install <plugin> oci://ghcr.io/konsole-is/camunda-operator-plugins/charts/<plugin> \
  --version <version> --namespace camunda-operator-plugins --create-namespace -f values.yaml
```

The fleet manager of the operator lists the official charts with the command for each one. Every plugin of a release has the same version.

The page of each plugin in [`docs/`](docs/index.md) gives its prerequisites and its chart values. The chart `aws` turns on no controller by default. Its values file turns on the controllers that you need, as [AWS plugins](docs/plugins/aws.md) shows.

## Layout

```
go.mod              github.com/konsole-is/camunda-operator-plugins
pkg/pluginkit/      the shared library of every plugin
pkg/                code that two or more plugins use
cmd/<plugin>/       the main package of each plugin
internal/<plugin>/  the controller of each plugin, or the shared manager of a plugin with more than one controller
internal/<plugin><controller>/
                    each controller of a plugin with more than one controller, for example internal/awskms/
charts/<plugin>/    the chart of each plugin
test/e2e/           the journeys, behind the e2e build tag
docs/               one page for each plugin and for each controller of the aws plugin, and the pluginkit page
hack/               the tools of the Makefile
```

The `gateway-access` plugin serves the access section: it gives a cluster or a management plane host names, and routes them through a Gateway API Gateway. The `rustfs` plugin serves the backup storage section: it gives each cluster its own RustFS bucket and access key. The `aws` plugin runs five controllers, and each one serves its own section. `aws-identity` serves the cloud identity section: it gives each cluster on EKS one IAM role, and puts the grants of other plugins on it. `aws-connectors-role` serves the Connectors permissions section: it gives the Connectors of each cluster on EKS an IAM role of their own. `aws-kms` serves the encryption key section: it gives each cluster one KMS key with automatic rotation. `aws-ebs` serves the volume encryption section: it gives each cluster on EKS a StorageClass that encrypts its EBS volumes with the KMS key of the cluster. `aws-s3` serves the backup storage section too: it gives each cluster its own S3 bucket. [`docs/pluginkit.md`](docs/pluginkit.md) describes the library for plugin authors.

## Development

`.tool-versions` gives the versions of Go, Helm, and cosign. `make lint-renovate` also needs `npx`, and the image targets need Docker.

| Command | What it does |
| --- | --- |
| `make test` | Runs the unit tests and the envtest suites. It downloads the envtest binaries and the CRD bases of the operator commit that `go.mod` names. |
| `make lint` | Runs golangci-lint. |
| `make chart-verify` | Lints and renders the chart of each plugin, and checks the chart rule. |
| `make build` | Builds the binary of each plugin into `bin/`. |
| `make docker-build PLUGIN=<plugin>` | Builds the image of one plugin, for example `make docker-build PLUGIN=aws`. |
| `make help` | Lists every target. |

## Contributing

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) before you add a plugin. It gives the rules for approval, code owners, deprecation, and charts.

## License

Apache-2.0. See [`LICENSE`](LICENSE).
