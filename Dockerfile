ARG GO_VERSION=1.26

FROM golang:${GO_VERSION} AS builder

WORKDIR /workspace
COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . /workspace

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o kube-oidc-discovery-proxy ./cmd/kube-oidc-discovery-proxy

FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /

COPY --from=builder /workspace/kube-oidc-discovery-proxy /usr/local/bin/kube-oidc-discovery-proxy

ENTRYPOINT ["/usr/local/bin/kube-oidc-discovery-proxy"]
