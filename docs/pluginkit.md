# pluginkit

`pkg/pluginkit` is the shared library of every plugin. A plugin implements three methods. pluginkit does the work that the plugin contract of the operator asks of each plugin: it publishes the registrations, serves the requests, writes the status, and applies the deletion policy. This page is for the author of a plugin.

## What a plugin implements

A plugin implements `pluginkit.Plugin[P, I]`. `P` is the Go type of the parameters of a request, and `I` is the Go type of its inputs. pluginkit decodes both from JSON.

```go
type Plugin[P, I any] interface {
	Reconcile(ctx context.Context, req pluginkit.Request[P, I]) (pluginkit.Result, error)
	Delete(ctx context.Context, req pluginkit.Request[P, I]) error
	Orphan(ctx context.Context, req pluginkit.Request[P, I]) error
}
```

- `Reconcile` brings the resources of a request to their desired state. It returns a `Result`: the outputs, the Kubernetes objects to apply, and the status fields. pluginkit calls it only for a request whose parameters pass the schema of the registration.
- `Delete` deletes what pluginkit does not delete itself: each resource outside the cluster, and each cluster-scoped object, by name. It must succeed when the resources are already gone.
- `Orphan` releases the resources of a request with the deletion policy `Orphan`, for example the tags that tie a cloud resource to the request.

`Request.Object` is a copy of the `PluginRequest`. `Request.Log` is a logger with the key `request`. Call `Request.Event` once for each external action, such as the create of a bucket.

The fields of `Result`:

| Field | What pluginkit does with it |
| --- | --- |
| `Outputs` | Writes them to `status.outputs`. Each key must be an output of the registration. Another key is an error. |
| `Objects` | Applies them with server-side apply, and adds the labels `plugins.camunda.io/registration` and `plugins.camunda.io/request`. A namespaced object gets an owner reference to the request and must be in the namespace of the request. A cluster-scoped object gets the labels only. |
| `Pending` | A `*pluginkit.Waiting` with a `Reason` and a `Message`. When set, the outputs are not ready. `OutputsReady` and `Ready` are `False` with its reason, and the outputs of the last reconcile stay. pluginkit applies no object and no Grant, and keeps the Grants that exist. It still writes `Summary`, `Links`, `PodSelector`, and `Resources`. Set `RequeueAfter` too, to look again. |
| `NotReady` | A `*pluginkit.Waiting`. When set, `Ready` is `False` with its reason. `OutputsReady` comes from the applied objects. |
| `Summary`, `Links`, `PodSelector`, `Resources` | Writes them to the status fields of the same name. `status.summaryTime` changes only when the summary changes. |
| `RequeueAfter` | Reconciles the request again after this time. Use it to wait for a resource outside the cluster. |

To start a plugin, call `pluginkit.Setup` with the manager, a `Config`, and the plugin. The `internal/skeleton` package is a complete example.

```go
err := pluginkit.Setup(mgr, pluginkit.Config{
	Name:           "s3-bucket",
	Version:        version.Version(),
	Registrations:  registrations,  // from pluginkit.LoadRegistrations
	ServiceAccount: serviceAccount, // from pluginkit.ServiceAccountFromEnv
	Owns:           []client.Object{&corev1.ConfigMap{}},
	Health:         checkCredentials,
}, plugin)
```

`Owns` holds one object of each kind that `Result.Objects` returns, namespaced and cluster-scoped. A change to an object of a namespaced kind starts a reconcile of its request. pluginkit does not watch a cluster-scoped kind of `Owns`. To wait for one, add a `Watch` or return `RequeueAfter`. `Health` checks the plugin, for example its cloud credentials. A nil `Health` is always healthy.

`Watches` starts a reconcile when an object that the plugin does not own changes. Examples are a Secret that a request names, the target `CamundaCluster`, and a cluster-scoped object that the plugin applies. A `pluginkit.Watch` holds one object of the kind to watch, and a `Requests` function. The function maps a changed object to the requests to reconcile. pluginkit ignores a request of a registration that the plugin does not serve. `Setup` refuses a `Watch` without an `Object` or a `Requests` function.

Each kind of `Watches` must be in the scheme of the manager. The manager caches each object of the kind in the cluster, and the ServiceAccount of the plugin needs `list` and `watch` on the kind. This `Watch` reconciles each request whose parameters name a changed Secret. `secretOf` is a function of the plugin that reads the name of the Secret from the parameters of a request.

```go
c := mgr.GetClient()
err := pluginkit.Setup(mgr, pluginkit.Config{
	// ...
	Watches: []pluginkit.Watch{{
		Object: &corev1.Secret{},
		Requests: func(ctx context.Context, secret client.Object) []reconcile.Request {
			var list pluginsv1alpha1.PluginRequestList
			if err := c.List(ctx, &list, client.InNamespace(secret.GetNamespace())); err != nil {
				return nil
			}
			var requests []reconcile.Request
			for _, request := range list.Items {
				if secretOf(&request) == secret.GetName() {
					requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&request)})
				}
			}
			return requests
		},
	}},
}, plugin)
```

Use `pluginkit.NewLogger` for the logger of the manager. It writes JSON lines, also in development mode.

## What pluginkit does for a plugin

### Registrations

At startup, pluginkit applies each registration of the `Config` with server-side apply. The field manager is `camunda-operator-plugins`. pluginkit sets `spec.controller` from `Name` and `Version`, and `spec.serviceAccount` from `ServiceAccount`. When an apply fails, pluginkit logs the error and tries again. The wait starts at one second and doubles after each failure, up to one minute.

Then pluginkit runs `Health` at each `HealthInterval` (30 seconds by default). It sets `Ready` of each registration: `True` with reason `Healthy`, or `False` with reason `Unhealthy` and the error as message. It never changes `Accepted`, which the operator sets.

pluginkit serves no request before all registrations are applied.

### Requests

pluginkit serves each `PluginRequest` whose label `plugins.camunda.io/registration` names a registration of the `Config`. For each request, it does these steps in this order:

1. It validates `spec.parameters` against the schema of the registration, with the validator of the Kubernetes API server. The error names each failing field, for example `spec.parameters.size`. The `x-kubernetes-validations` rules of the schema are not checked.
2. It renders each parameter whose schema has `"x-fleet-template": true`.
3. It adds the finalizer `plugins.camunda.io/cleanup`. A request whose parameters never passed the schema has no finalizer, so its delete does not wait.
4. It calls `Reconcile`.
5. It renders the grant templates of the registration, and applies the objects and the Grants.
6. It deletes each Grant of the request that it no longer renders.
7. It writes the status of the request, once.

### Templates

A grant template, and a parameter marked with `"x-fleet-template": true`, can hold these placeholders and plain text only:

| Placeholder | Value |
| --- | --- |
| `{{ .namespace }}` | The namespace of the request. |
| `{{ .claim }}` | The name of the claim of the request. |
| `{{ .request }}` | The name of the request. |
| `{{ .serviceAccount }}` | `spec.target.serviceAccountName` of the request. |
| `{{ .parameters.<name> }}` | One parameter of the request. |
| `{{ .outputs.<key> }}` | One output that `Reconcile` returned. A template parameter is rendered before `Reconcile`, so it has no outputs. |

A metric query can hold the same placeholders and also `{{ .pods }}`, the pods of the request as a regular expression. A grant template and a parameter cannot hold `{{ .pods }}`. Every other construct is an error that names it, for example `{{ if }}`, `{{ define }}`, a comment, a pipeline, a function, a variable, or an unknown placeholder. A placeholder whose parameter or output has no value is an error too. `pluginkit.ParseTemplate` checks a template, so a plugin can test the templates and the metric queries of its chart.

pluginkit renders each string of the JSON of a template on its own. A value with a quote stays inside its string.

### Grants

pluginkit writes one `Grant` for each grant template of the registration. It skips a template when an input of its `requiresInputs` has no value in `spec.inputs`. The name of the Grant is `pluginkit.GrantName(<request>, <template>)`, and it has at most 63 characters. Each Grant has an owner reference to the request and the labels `plugins.camunda.io/registration`, `plugins.camunda.io/request`, and `plugins.camunda.io/cluster`. A registration with grant templates needs a request with the target kind `CamundaCluster`. For another target kind, pluginkit writes no Grant and sets `Ready=False` with reason `GrantTemplateInvalid`.

When an input loses its value, or a template leaves the registration, pluginkit deletes the Grant.

### Status

pluginkit writes the status of the request once for each reconcile. The `Ready` condition comes from the first rule that matches:

1. `OutputsReady` is not `True`: `Ready` is `False` with the reason of `OutputsReady`.
2. A Grant of the request has `Ready=False`: `Ready` is `False` with reason `GrantRejected`. The message names the Grant and its reason. A Grant without a `Ready` condition does not count.
3. `Result.NotReady` is set: `Ready` is `False` with its reason.
4. Else `Ready` is `True` with reason `Healthy`.

When another writer changed the request after the read, the status write fails with a conflict. pluginkit then reconciles the request again after one second.

### Deletion

When a request is deleted, pluginkit calls `Delete` or `Orphan`, by `spec.deletionPolicy`. It decodes the parameters without the schema check, so a changed schema does not block the delete. When the template parameters render, the plugin gets the same values as in `Reconcile`. When the parameters do not decode into `P`, the plugin cannot apply the policy: the finalizer stays, and `Ready` is `False` with reason `InvalidParameters`. Correct `spec.parameters`, and the delete continues.

- `Delete`: pluginkit deletes each Grant with the label `plugins.camunda.io/request: <request>` in the namespace of the request. It does not rely on the owner reference, because a user can remove it. The garbage collector deletes the namespaced objects.
- `Orphan`: pluginkit removes the owner references to the request from the Grants and from the namespaced objects of `Owns`. They keep their labels. A cluster-scoped object has no owner reference, so it stays as it is.

The finalizer stays until the method of the plugin returns nil and each Grant is gone. When the method returns an error, `Ready` is `False` with reason `DeleteFailed`, and pluginkit calls the method again with the backoff of controller-runtime. A Grant can hold a finalizer of the identity extension, and pluginkit waits for it.

### Reasons

pluginkit uses the reasons of the api module, and adds these reasons of its own:

| Reason | Condition | Meaning |
| --- | --- | --- |
| `InvalidParameters` | `OutputsReady`, `Ready` | The parameters do not pass the schema, or a template parameter does not render. The message names the field. |
| `ReconcileError` | `Ready` | `Reconcile` returned an error, or an output key that the registration does not declare. |
| `GrantTemplateInvalid` | `Ready` | A grant template does not render for the request. |
| `DeleteFailed` | `Ready` | `Delete` or `Orphan` returned an error. The finalizer stays. |
| `Unhealthy` | `Ready` of a registration | `Health` returned an error. |

## The chart of a plugin

The chart value `registrations` is a list. Each item is a name and the fields of a `PluginRegistration` spec. The chart writes the list to a ConfigMap, mounts it in the controller, and the controller reads it with `pluginkit.LoadRegistrations`. An unknown field is an error at startup.

```yaml
registrations:
  - name: s3-bucket
    sections: [backup-storage]
    label: New S3 bucket
    mode: Default
    deletionPolicies: [Delete, Orphan]
    outputs:
      - key: bucket
```

A rolling update runs the old and the new controller together, so the chart turns on leader election: only the replica that holds the Lease publishes the registrations and serves the requests. The skeleton passes `-leader-elect` and `-leader-election-id=<release>`, and ships a Role and a RoleBinding for `leases` in the release namespace, as `charts/skeleton/templates/leader-election.yaml` shows. `make chart-verify` checks both.

The chart sets the variables `POD_NAMESPACE` and `SERVICE_ACCOUNT_NAME` from the downward API. `pluginkit.ServiceAccountFromEnv` reads them.

The controller needs this RBAC, as `charts/skeleton/templates/rbac.yaml` shows:

| Resource | Verbs |
| --- | --- |
| `pluginregistrations`, with `resourceNames` set to the registrations | `get`, `create`, `patch` |
| `pluginregistrations/status`, with the same `resourceNames` | `update` |
| `pluginrequests` | `get`, `list`, `watch`, `update`, `patch` |
| `pluginrequests/status` | `update` |
| `pluginrequests/finalizers` | `update`, because the owner references of the objects block the delete of the request |
| `grants` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `events.k8s.io` `events` | `create`, `patch` |
| each kind of `Owns` | `get`, `list`, `watch`, `create`, `update`, `patch` |
| each kind of `Watches` | `list`, `watch`, and each other verb that the plugin uses on the kind |
| `coordination.k8s.io` `leases`, in the release namespace only | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |

The chart also keeps the `serve` role of the chart rule in [`CONTRIBUTING.md`](../CONTRIBUTING.md).
