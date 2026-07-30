package main

import (
	"testing"
)

func TestParseFlagsDefaults(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if cfg.BindAddress != ":8080" || cfg.LogLevel != "info" || len(cfg.Routes) != 0 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
}

func TestParseFlags(t *testing.T) {
	cfg, err := parseFlags([]string{
		"--bind-address", ":9090",
		"--log-level", "debug",
		"--target", "one.proxy.test=one.example.com",
		"--target", "two.proxy.test=two.example.com",
	})
	if err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	if cfg.BindAddress != ":9090" || cfg.LogLevel != "debug" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	wantRoutes := []route{
		{Host: "one.proxy.test", Upstream: "one.example.com"},
		{Host: "two.proxy.test", Upstream: "two.example.com"},
	}
	if len(cfg.Routes) != len(wantRoutes) {
		t.Fatalf("got %d routes, want %d", len(cfg.Routes), len(wantRoutes))
	}
	for i := range wantRoutes {
		if cfg.Routes[i] != wantRoutes[i] {
			t.Errorf("route %d: got %#v, want %#v", i, cfg.Routes[i], wantRoutes[i])
		}
	}
}

func TestParseFlagsRejectsInvalidTarget(t *testing.T) {
	if _, err := parseFlags([]string{"--target", "invalid"}); err == nil {
		t.Fatal("expected invalid target error")
	}
}
