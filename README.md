# kube-oidc-discovery-proxy

A minimal reverse proxy that exposes only the OIDC discovery document and JWKS of upstream Kubernetes apiservers, so a consumer can validate projected service account tokens minted by those clusters.

This is mainly intended for on-prem environments where exposing an ingress for the entire apiserver is undesirable or not possible. Instead of opening up the apiserver, the proxy forwards just the two endpoints needed for token validation.

Only these paths are forwarded per configured host; everything else returns 404:

- `/.well-known/openid-configuration`
- `/openid/v1/jwks`

## How it works

Each upstream apiserver is exposed under its own predictable host. Requests are routed by `Host` header to a preconfigured upstream — the upstream is never derived from the request, so it cannot be redirected elsewhere.

The proxy reaches these endpoints unauthenticated, so the upstream cluster must grant the `system:unauthenticated` group access to the discovery role:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: unauthenticated-oidc-discovery
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:service-account-issuer-discovery
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: system:unauthenticated
```

```sh
kube-oidc-discovery-proxy \
  --bind-address :8080 \
  --target apiserver-oidc-dev-fss.example.com=apiserver.dev-fss.nais.io \
  --target apiserver-oidc-prod-fss.example.com=apiserver.prod-fss.nais.io
```

| Flag | Description | Default |
|------|-------------|---------|
| `--bind-address` | Address to listen on | `:8080` |
| `--target` | `host=upstream` route, repeatable | — |
| `--log-level` | Log level (`debug`, `info`, `warn`, `error`) | `info` |

## Local development

```sh
mise install      # install tools
mise run test     # run tests
mise run check    # static analysis + helm lint
mise run fmt      # format
```

See `mise run --list` for all available tasks.
