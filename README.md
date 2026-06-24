# kube-oidc-discovery-proxy

A minimal reverse proxy that exposes the OIDC discovery document and JWKS of upstream Kubernetes apiservers, so a consumer can validate projected service account tokens minted by those clusters.

Only two paths are forwarded per configured host; everything else returns 404:

- `/.well-known/openid-configuration`
- `/openid/v1/jwks`

## How it works

Each upstream apiserver is exposed under its own predictable host. Requests are routed by `Host` header to a preconfigured upstream — the upstream is never derived from the request, so it cannot be redirected elsewhere.

```sh
kube-oidc-discovery-proxy \
  --bind-address :8080 \
  --target dev-fss-apiserver-oidc.example.com=apiserver.dev-fss.nais.io \
  --target prod-fss-apiserver-oidc.example.com=apiserver.prod-fss.nais.io
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
