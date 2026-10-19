# Skeleton

The skeleton plugin is the plugin that a new plugin copies. It serves its registrations through [pluginkit](../pluginkit.md) and writes one ConfigMap for each request. Its default section `skeleton` does not exist, so the operator does not accept the registration and the fleet manager never offers it. A new plugin sets the sections of its registrations to sections that exist.

## What it creates

- One PluginRegistration for each item of the chart value `registrations`.
- One ConfigMap `<request>-skeleton` for each PluginRequest, in the namespace of the request.
- One Lease `<release>` in the release namespace. Only the replica that holds it writes, so a rolling update never runs two writers.

## Prerequisites

The skeleton needs the CRDs of the operator on the cluster. Its envtest suite loads the CRD bases of the operator, and `make test` downloads them.

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `registrations` | one registration `skeleton` for the section `skeleton` | The PluginRegistrations that the controller writes at startup and serves. Each item is a name and the fields of a registration spec. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/skeleton` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

## Parameters

The skeleton has no parameters, so a claim can change nothing.

## Outputs

| Key | Value |
| --- | --- |
| `configMapName` | The name of the ConfigMap that the skeleton writes for the request. |

## Grants

The skeleton writes no grants.

## Deletion

With the deletion policy `Delete`, the garbage collector deletes the ConfigMap with the request. With `Orphan`, the ConfigMap stays, without its owner reference to the request.

## Troubleshooting

| Reason | Condition | What to do |
| --- | --- | --- |
| `InvalidParameters` | `OutputsReady` and `Ready` of the request | The default registration has no schema, so it does not get this reason. A registration of the chart values that adds a `parameters` schema gets it when the parameters of the request do not pass the schema. The message names the field. Correct the parameters of the claim, or the schema. |
| `ReconcileError` | `Ready` of the request | The message gives the error. For the skeleton, the usual cause is a registration that does not declare the output `configMapName`. Add it to the `outputs` of the registration in the chart values. |
| `DeleteFailed` | `Ready` of a deleted request | pluginkit could not list or delete the Grants of the request, or could not remove an owner reference. The message gives the error. The finalizer stays, and pluginkit tries again. |

The skeleton has no health check, so its registration is never `Unhealthy`.

When the controller Pod does not get ready, read its log. The controller stops when it cannot read the file of its registrations, or when the variable `POD_NAMESPACE` or `SERVICE_ACCOUNT_NAME` is not set.
