# Gateway access

The gateway-access plugin serves the `access` section of the operator. It gives a cluster or a management plane its host names, and it routes them through a [Gateway API](https://gateway-api.sigs.k8s.io/) Gateway. It serves through [pluginkit](../pluginkit.md).

The chart has one registration for each Gateway. The defaults are `gateway-public` and `gateway-internal`.

## What it creates

The plugin reads `status.endpoints` of the target of each request, and writes routes to the Services of those endpoints:

- For a `CamundaCluster`: one `HTTPRoute <request>` on the cluster host, with one rule for each HTTP endpoint, such as `/operate` or `/v2`. One `GRPCRoute <request>-grpc` on the gRPC host.
- For the `CamundaOptimize` of that cluster: one `HTTPRoute <request>-optimize` on the Optimize host.
- For a `CamundaManagementCluster`: one `HTTPRoute <request>` on the management host, with one rule for each endpoint, such as `/auth` or `/modeler`.

Each route is in the namespace of the claim, next to its Services. Its only parent is the Gateway of `gatewayRef`, on the listener `listenerName` when it is set. The routes are standard-channel `gateway.networking.k8s.io/v1` objects.

A route counts as accepted only when the controller of the GatewayClass of that Gateway accepts it. The plugin ignores the route status that another controller wrote. For example, a Gateway that was recreated with the same name under another implementation leaves the status of the old controller on the routes.

When an endpoint leaves `status.endpoints`, or the Optimize is deleted, the plugin deletes the route at the next reconcile.

The plugin also writes:

- One PluginRegistration for each item of the chart value `registrations`.
- One Lease `<release>` in the release namespace. Only the replica that holds it writes.

The summary of a request shows the host, the number of accepted routes, and the expiry date of the listener certificate. The expiry has the severity `Warning` when the certificate expires within 21 days.

## Prerequisites

Before you install the chart, make sure that the cluster has:

1. A Gateway API implementation that supports `HTTPRoute` and `GRPCRoute` of the standard channel, with their CRDs.
2. A Gateway for each registration, with an HTTPS listener and its certificate. The plugin does not manage certificates.
3. `allowedRoutes` on that listener that admits the namespaces of the claims.
4. DNS records for the hostname patterns that point at the Gateway.
5. The CRDs of the operator.

## Chart values

| Value | Default | Meaning |
| --- | --- | --- |
| `registrations` | `gateway-public` on the Gateway `gateway-system/public`, and `gateway-internal` on `gateway-system/internal` | The PluginRegistrations that the controller writes at startup and serves, one for each Gateway. Each item is a name and the fields of a registration spec. Set `gatewayRef` and the hostname patterns in `parameterValues`. The chart gives the controller the verb `serve` on each name and on no other registration. The chart refuses to render without one. |
| `reportCertificateExpiry` | `true` | Show the expiry of the listener certificate in the summary of each `https` request. It grants `get` on the Secrets of `certificateSecrets`. |
| `certificateSecrets` | `[]` | The Secret of each listener certificate, as `{namespace, name}`. The chart grants `get` on these Secrets only. The expiry of a certificate whose Secret is not in the list shows as `unknown`. |
| `image.repository` | `ghcr.io/konsole-is/camunda-operator-plugins/gateway-access` | The image of the controller. |
| `image.tag` | the `appVersion` of the chart | The tag of the image. |
| `resources` | 10m CPU and 64Mi memory requested, 128Mi memory limit | The resources of the controller container. |

The value `registrations` replaces the default list. For example, these values serve one registration on the Gateway `ingress/edge`, with hosts such as `orders.team-a.apps.acme.io`, and report the expiry of its certificate:

```yaml
registrations:
  - name: gateway-edge
    sections: [access]
    label: Access through the edge Gateway
    mode: Optional
    deletionPolicies: [Delete]
    outputs:
      - key: externalUrl
      - key: grpcHost
      - key: optimizeUrl
      - key: managementHost
    parameters:
      type: object
      required: [gatewayRef, hostnamePattern, grpcHostnamePattern, optimizeHostnamePattern, managementHostnamePattern]
      properties:
        gatewayRef:
          type: object
          required: [namespace, name]
          properties:
            namespace: {type: string}
            name: {type: string}
        listenerName: {type: string}
        scheme: {type: string, enum: [http, https]}
        hostnamePattern: {type: string}
        grpcHostnamePattern: {type: string}
        optimizeHostnamePattern: {type: string}
        managementHostnamePattern: {type: string}
    parameterValues:
      gatewayRef: {namespace: ingress, name: edge}
      listenerName: https
      hostnamePattern: "{name}.{namespace}.apps.acme.io"
      grpcHostnamePattern: "grpc-{name}.{namespace}.apps.acme.io"
      optimizeHostnamePattern: "optimize-{name}.{namespace}.apps.acme.io"
      managementHostnamePattern: "{name}.{namespace}.apps.acme.io"
certificateSecrets:
  - namespace: ingress
    name: edge-tls
```

The `values.yaml` of the chart has the full schema of the default registrations, with the length limits of each parameter.

## Parameters

The admin sets the parameters in `parameterValues` of each registration. An environment can override each of them. A claim cannot change a parameter unless the environment marks it `claimEditable`.

A hostname pattern takes two placeholders only: `{name}`, the name of the claim, and `{namespace}`, the namespace of the claim.

| Parameter | Required | Meaning |
| --- | --- | --- |
| `gatewayRef.namespace`, `gatewayRef.name` | yes | The Gateway that the routes attach to. |
| `listenerName` | no | The listener of the Gateway. Without it, the routes attach to the Gateway, and the expiry comes from the first HTTPS listener with a certificate. |
| `scheme` | no | `http` or `https`. The default is `https`. With `http`, the summary shows no certificate expiry. |
| `hostnamePattern` | yes | The host of the web applications and the REST API of a cluster, for example `{name}.{namespace}.example.com`. |
| `grpcHostnamePattern` | yes | The host of the gRPC API of a cluster. |
| `optimizeHostnamePattern` | yes | The host of Optimize of a cluster. |
| `managementHostnamePattern` | yes | The host of a management plane. |

Each host must be a DNS name and not an IP address. Each label is at most 63 characters, and the host is at most 253. A long claim name and a long namespace can give a label that is too long.

## Outputs

Every request gets all four outputs, also a request of a management plane. Each claim reads only the outputs of its own kind.

| Key | Value | Read by |
| --- | --- | --- |
| `externalUrl` | `<scheme>://<host of hostnamePattern>` | a cluster claim |
| `grpcHost` | the host of `grpcHostnamePattern` | a cluster claim |
| `optimizeUrl` | `<scheme>://<host of optimizeHostnamePattern>` | a cluster claim |
| `managementHost` | the host of `managementHostnamePattern` | a management plane claim |

The plugin publishes the outputs before the target exists, so the claim can configure the target with its URLs.

## Grants

The plugin writes no grants.

## Deletion

The registrations offer the deletion policy `Delete` only. When a claim is deleted, the plugin deletes every route of its request. A route to a Service that is gone never stays.

## Troubleshooting

| Reason | Condition | What to do |
| --- | --- | --- |
| `WaitingForEndpoints` | `Ready` of the request | The target does not exist yet, or it has published no endpoints yet. The outputs are ready. Wait until the operator reconciles the target. |
| `GatewayNotFound` | `Ready` of the request | The Gateway of `gatewayRef` does not exist, or its GatewayClass does not exist. The message names the missing object. Correct `gatewayRef`, or create the Gateway. |
| `Pending` | `Ready` of the request | The controller of the GatewayClass has not reported on a route yet. The message names the route. Make sure that this controller runs. |
| `NotAllowedByListeners` and the other route reasons | `Ready` of the request | The Gateway refused a route. The message names the route and gives the message of the Gateway. For `NotAllowedByListeners`, make sure that `allowedRoutes` of the listener admits the namespace of the claim. For `BackendNotFound`, make sure that the Service of the target exists. |
| `InvalidParameters` | `OutputsReady` and `Ready` of the request | The parameters do not pass the schema, or a pattern gives a host that is not a DNS name. The message names the parameter and the host. Correct the pattern in the registration or the environment. For a label that is too long, put `{name}` and `{namespace}` in two labels, as in `{name}.{namespace}.example.com`, or use a shorter claim name. |
| `UnsupportedTarget` | `OutputsReady` and `Ready` of the request | The target kind is not `CamundaCluster` or `CamundaManagementCluster`. |
| `ReconcileError` | `Ready` of the request | The message gives the error, for example a read of the target, of the Gateway, or of its GatewayClass, or a list of the routes that failed. pluginkit tries again. |
| `DeleteFailed` | `Ready` of a deleted request | The plugin could not delete a route of the request. The message gives the error. The finalizer stays, and pluginkit tries again. |

A summary item `Certificate expires` with the value `unknown: <cause>` means that the plugin could not read the certificate. The request is still `Ready`. Usual causes:

- The Secret is not in `certificateSecrets`, so the controller has no `get` on it.
- `listenerName` names a listener without a certificate.
- The certificate reference of the listener is not a core Secret, for example an object of the Gateway implementation.
- The Secret holds no PEM certificate in `tls.crt`.

Add the Secret to `certificateSecrets`, set `listenerName` to the HTTPS listener, or correct the Secret.

The plugin has no health check, so its registration is never `Unhealthy`.

When the controller Pod does not get ready, read its log. The controller stops when it cannot read the file of its registrations, or when the variable `POD_NAMESPACE` or `SERVICE_ACCOUNT_NAME` is not set.
