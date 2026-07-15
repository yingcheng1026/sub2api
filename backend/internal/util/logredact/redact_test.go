package logredact

import (
	"strings"
	"testing"
)

func TestRedactText_JSONLike(t *testing.T) {
	in := `{"access_token":"ya29.a0AfH6SMDUMMY","refresh_token":"1//0gDUMMY","other":"ok"}`
	out := RedactText(in)
	if out == in {
		t.Fatalf("expected redaction, got unchanged")
	}
	if want := `"access_token":"***"`; !strings.Contains(out, want) {
		t.Fatalf("expected %q in %q", want, out)
	}
	if want := `"refresh_token":"***"`; !strings.Contains(out, want) {
		t.Fatalf("expected %q in %q", want, out)
	}
}

func TestRedactText_QueryLike(t *testing.T) {
	in := "access_token=ya29.a0AfH6SMDUMMY refresh_token=1//0gDUMMY"
	out := RedactText(in)
	if strings.Contains(out, "ya29") || strings.Contains(out, "1//0") {
		t.Fatalf("expected tokens redacted, got %q", out)
	}
}

func TestRedactTextAPIKeyFields(t *testing.T) {
	in := `{"api_key":"sk-secret-value","custom_key":"custom-secret","verification":"fresh-password","other":"ok"}`
	out := RedactText(in)
	if strings.Contains(out, "sk-secret-value") || strings.Contains(out, "custom-secret") || strings.Contains(out, "fresh-password") {
		t.Fatalf("expected API key fields redacted, got %q", out)
	}
}

func TestRedactText_GOCSPX(t *testing.T) {
	in := "client_secret=GOCSPX-your-client-secret"
	out := RedactText(in)
	if strings.Contains(out, "your-client-secret") {
		t.Fatalf("expected secret redacted, got %q", out)
	}
	if !strings.Contains(out, "client_secret=***") {
		t.Fatalf("expected key redacted, got %q", out)
	}
}

func TestRedactText_ExtraKeyCacheUsesNormalizedSortedKey(t *testing.T) {
	clearExtraTextPatternCache()

	out1 := RedactText("custom_secret=abc", "Custom_Secret", " custom_secret ")
	out2 := RedactText("custom_secret=xyz", "custom_secret")
	if !strings.Contains(out1, "custom_secret=***") {
		t.Fatalf("expected custom key redacted in first call, got %q", out1)
	}
	if !strings.Contains(out2, "custom_secret=***") {
		t.Fatalf("expected custom key redacted in second call, got %q", out2)
	}

	if got := countExtraTextPatternCacheEntries(); got != 1 {
		t.Fatalf("expected 1 cached pattern set, got %d", got)
	}
}

func TestRedactText_DefaultPathDoesNotUseExtraCache(t *testing.T) {
	clearExtraTextPatternCache()

	out := RedactText("access_token=abc")
	if !strings.Contains(out, "access_token=***") {
		t.Fatalf("expected default key redacted, got %q", out)
	}
	if got := countExtraTextPatternCacheEntries(); got != 0 {
		t.Fatalf("expected extra cache to remain empty, got %d", got)
	}
}

func TestRedactTextRemovesProxyURLUserInfo(t *testing.T) {
	input := `proxy=http://alice:p%40ssword@proxy.example.com:8080/path?mode=ok`
	out := RedactText(input)
	if strings.Contains(out, "alice") || strings.Contains(out, "ssword") {
		t.Fatalf("expected proxy userinfo removed, got %q", out)
	}
	if !strings.Contains(out, "http://proxy.example.com:8080/path?mode=ok") {
		t.Fatalf("expected non-secret endpoint retained, got %q", out)
	}
}

func TestRedactJSONRemovesProxyURLUserInfoFromValues(t *testing.T) {
	input := `{"proxy":"socks5h://alice:secret@socks.example.com:1080","other":"ok"}`
	out := RedactText(input)
	if strings.Contains(out, "alice") || strings.Contains(out, "secret") {
		t.Fatalf("expected proxy userinfo removed from JSON value, got %q", out)
	}
	if !strings.Contains(out, "socks5h://socks.example.com:1080") {
		t.Fatalf("expected non-secret endpoint retained, got %q", out)
	}
}

func clearExtraTextPatternCache() {
	extraTextPatternCache.Range(func(key, value any) bool {
		extraTextPatternCache.Delete(key)
		return true
	})
}

func countExtraTextPatternCacheEntries() int {
	count := 0
	extraTextPatternCache.Range(func(key, value any) bool {
		count++
		return true
	})
	return count
}
