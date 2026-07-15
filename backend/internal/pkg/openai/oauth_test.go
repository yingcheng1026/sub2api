package openai

import (
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionStore_Stop_Idempotent(t *testing.T) {
	store := NewSessionStore()

	store.Stop()
	store.Stop()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

func TestSessionStore_Stop_Concurrent(t *testing.T) {
	store := NewSessionStore()

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Stop()
		}()
	}

	wg.Wait()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

func TestBuildAuthorizationURLForPlatform_OpenAI(t *testing.T) {
	authURL := BuildAuthorizationURLForPlatform("state-1", "challenge-1", DefaultRedirectURI, OAuthPlatformOpenAI)
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("Parse URL failed: %v", err)
	}
	q := parsed.Query()
	if got := q.Get("client_id"); got != ClientID {
		t.Fatalf("client_id mismatch: got=%q want=%q", got, ClientID)
	}
	if got := q.Get("codex_cli_simplified_flow"); got != "true" {
		t.Fatalf("codex flow mismatch: got=%q want=true", got)
	}
	if got := q.Get("id_token_add_organizations"); got != "true" {
		t.Fatalf("id_token_add_organizations mismatch: got=%q want=true", got)
	}
}

func TestBuildAuthorizationURLForPlatform_XAI(t *testing.T) {
	authURL := BuildAuthorizationURLForPlatform("state-1", "challenge-1", "", OAuthPlatformXAI)
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	require.Equal(t, XAIAuthorizeURL, parsed.Scheme+"://"+parsed.Host+parsed.Path)
	require.Equal(t, XAIClientID, parsed.Query().Get("client_id"))
	require.Equal(t, XAIDefaultRedirectURI, parsed.Query().Get("redirect_uri"))
	require.Equal(t, XAIScopes, parsed.Query().Get("scope"))
	require.Equal(t, "generic", parsed.Query().Get("plan"))
	require.Equal(t, "opencode", parsed.Query().Get("referrer"))
	require.NotEmpty(t, parsed.Query().Get("nonce"))
	require.Empty(t, parsed.Query().Get("codex_cli_simplified_flow"))
}

func TestOAuthProviderConfigByClientID_XAI(t *testing.T) {
	cfg := OAuthProviderConfigByClientID(XAIClientID)
	require.Equal(t, OAuthPlatformXAI, cfg.Platform)
	require.Equal(t, XAITokenURL, cfg.TokenURL)
	require.Empty(t, cfg.RefreshScopes)
}
