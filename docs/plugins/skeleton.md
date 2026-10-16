# Skeleton

The skeleton plugin is the plugin that a new plugin copies. It starts a controller that serves its health probes. The scheme of the controller holds the `plugins.camunda.io` kinds, but the controller reads nothing from the cluster. It serves no section yet.

## What it creates

The skeleton creates nothing.

## Prerequisites

The skeleton needs nothing on the cluster, because it reads nothing. Its envtest suite loads the CRD bases of the operator, and `make test` downloads them.

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `registrations` | `[{name: skeleton}]` | The names of the PluginRegistrations that the controller serves. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/skeleton` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

## Parameters

The skeleton has no parameters, so a claim can change nothing.

## Outputs

The skeleton publishes no outputs.

## Grants

The skeleton writes no grants.

## Deletion

The skeleton has no resources to delete.

## Troubleshooting

The skeleton sets no conditions, so it has no reasons to troubleshoot. When the controller Pod does not get ready, read its log for the error that stopped the manager.
