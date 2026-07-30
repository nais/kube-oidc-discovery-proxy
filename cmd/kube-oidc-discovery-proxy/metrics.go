package main

import (
	"github.com/prometheus/client_golang/prometheus"
)

// newRegistry returns a dedicated Prometheus registry with no default collectors,
// and the upstream_up gauge pre-registered on it.
func newRegistry() (*prometheus.Registry, *prometheus.GaugeVec) {
	reg := prometheus.NewRegistry()
	upstreamUp := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kube_oidc_discovery_proxy_upstream_up",
		Help: "1 if the last fetch from the upstream path succeeded with HTTP 200, 0 otherwise.",
	}, []string{"upstream", "path"})
	reg.MustRegister(upstreamUp)
	return reg, upstreamUp
}
