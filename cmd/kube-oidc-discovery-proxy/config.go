package main

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// route maps an inbound request host to the upstream apiserver host it should
// be proxied to. Each apiserver is exposed under its own predictable host so a
// consumer can store one issuer URL per environment.
type route struct {
	Host     string
	Upstream string
}

type Config struct {
	BindAddress string
	LogLevel    string
	CacheTTL    time.Duration
	Routes      []route
}

func DefaultConfig() Config {
	return Config{
		BindAddress: ":8080",
		LogLevel:    "info",
		CacheTTL:    time.Minute,
	}
}

func parseFlags(args []string) (Config, error) {
	cfg := DefaultConfig()
	flags := flag.NewFlagSet("kube-oidc-discovery-proxy", flag.ContinueOnError)
	flags.StringVar(&cfg.BindAddress, "bind-address", cfg.BindAddress, "address to listen on")
	flags.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "which log level to output")
	flags.DurationVar(&cfg.CacheTTL, "cache-ttl", cfg.CacheTTL, "how long to cache upstream discovery responses")
	flags.Var(targets{routes: &cfg.Routes}, "target", "host=upstream route, repeatable")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// targets is a flag.Value accepting repeated host=upstream pairs.
type targets struct {
	routes *[]route
}

func (t targets) String() string {
	if t.routes == nil {
		return ""
	}
	parts := make([]string, 0, len(*t.routes))
	for _, r := range *t.routes {
		parts = append(parts, r.Host+"="+r.Upstream)
	}
	return strings.Join(parts, ",")
}

func (t targets) Set(v string) error {
	host, upstream, ok := strings.Cut(v, "=")
	if !ok || host == "" || upstream == "" {
		return fmt.Errorf("target must be host=upstream, got %q", v)
	}
	*t.routes = append(*t.routes, route{Host: host, Upstream: upstream})
	return nil
}
