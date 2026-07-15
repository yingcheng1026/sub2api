package service

import (
	"strings"
	"testing"
)

func TestCustomRelayURLNeverSerializesProxyCredentials(t *testing.T) {
	svc := &GatewayService{}
	got := svc.buildCustomRelayURL("https://relay.example.com/", "/v1/messages")
	if got != "https://relay.example.com/v1/messages?beta=true" {
		t.Fatalf("custom relay URL = %q", got)
	}
	if strings.Contains(got, "proxy=") || strings.Contains(got, "secret") {
		t.Fatalf("custom relay URL contains proxy material: %q", got)
	}
}

func TestCustomRelayRejectsAccountProxyCombination(t *testing.T) {
	proxyID := int64(9)
	account := &Account{
		ProxyID: &proxyID,
		Proxy: &Proxy{
			Protocol: "http",
			Host:     "proxy.example.com",
			Port:     8080,
			Username: "alice",
			Password: "proxy-secret",
		},
	}
	if err := validateCustomRelayProxyIsolation(account); err == nil {
		t.Fatal("custom relay accepted an account proxy")
	}

	account.ProxyID = nil
	if err := validateCustomRelayProxyIsolation(account); err != nil {
		t.Fatalf("proxy-free custom relay rejected: %v", err)
	}
}
