package service

import "testing"

func TestHashAPIKeyIsStableSHA256Hex(t *testing.T) {
	got := HashAPIKey("hfc_test_key")
	want := "73d1c9f908f87b8a69c8314ff24303ce3f97f8b55f840ac7de25cdf6b3079afd"
	if got != want {
		t.Fatalf("HashAPIKey() = %q, want %q", got, want)
	}
}

func TestAPIKeyPrefixForStorage(t *testing.T) {
	if got := APIKeyPrefixForStorage("short"); got != "short" {
		t.Fatalf("short prefix = %q", got)
	}

	if got := APIKeyPrefixForStorage("hfc_1234567890abcdef"); got != "hfc_12345678" {
		t.Fatalf("long prefix = %q", got)
	}
}
