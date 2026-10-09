# hetzner-bucket-controller Helm chart

Installs the Hetzner bucket controller: it reconciles `Bucket` objects into Hetzner Object Storage
buckets, with Object Lock, default retention, versioning and lifecycle rules. The chart ships the
`Bucket` CustomResourceDefinition and the controller's ClusterRole under `templates/`, both generated
from the Go source by `make manifests`, so `helm upgrade` updates them.

## Install

```console
helm upgrade --install hetzner-bucket-controller charts/hetzner-bucket-controller \
  --namespace object-storage --create-namespace \
  --set 'rbac.secretNamespaces={backup-buckets}'
```

## RBAC

| Object | Scope | Grants |
|---|---|---|
| ClusterRole, generated | cluster | `get`, `list`, `watch` on `buckets`; `get`, `update`, `patch` on `buckets/status`; `create`, `patch` on `events.k8s.io` Events |
| Role `<release>-secrets` | each namespace of `rbac.secretNamespaces` | `get` on Secrets |
| Role `<release>-leader-election` | release namespace | Leases, for leader election |

There is no cluster-wide access to Secrets and no `update` on `buckets` or `buckets/finalizers`: the
controller never adds a finalizer. See [ADR-0005](../../docs/decisions/0005-credentials-read-per-namespace.md).

## Values

| Key | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/maarlab-rethinking/hetzner-bucket-controller` | Controller image. |
| `image.tag` | `""` | Defaults to the chart's `appVersion`. |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | |
| `fullnameOverride` | `""` | Name of every resource. Defaults to the release name. |
| `replicas` | `1` | Leader-elected; more replicas add a hot standby, not throughput. |
| `controller.resyncPeriod` | `10m` | How often each `Bucket` is checked against Hetzner for drift and out-of-band deletion. |
| `controller.waitPeriod` | `1m` | Retry interval while a `Bucket` waits on its credentials Secret or on a name conflict. |
| `controller.operationTimeout` | `2m` | Upper bound for the S3 calls of one reconcile. |
| `controller.logLevel` | `info` | `debug`, `info` or `error`. |
| `controller.extraArgs` | `[]` | Extra command-line flags. |
| `resources` | 10m / 64Mi requests, 200m / 128Mi limits | |
| `nodeSelector`, `tolerations`, `affinity` | empty | |
| `podAnnotations` | `{}` | |
| `priorityClassName` | `""` | |
| `installCRD` | `true` | Install the `Bucket` CRD. Set `false` if it is managed separately. |
| `rbac.create` | `true` | Create the ClusterRole, the Roles and their bindings. Set `false` to bring your own. |
| `rbac.secretNamespaces` | `[]` | Namespaces whose Secrets the controller may `get`, one Role each. List every namespace that holds `Bucket` objects; elsewhere they report `SecretForbidden`. Empty means the release namespace only. |
| `serviceAccount.create` | `true` | |
| `serviceAccount.name` | `""` | Defaults to the full name. |
| `serviceAccount.annotations` | `{}` | |

The metrics endpoint listens on port 8080 (plain HTTP) and the health probes on 8081.
