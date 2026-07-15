package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEasyPayDoesNotForwardSecretFormAcrossRedirect(t *testing.T) {
	var redirectedCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedCalls.Add(1)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "secret-pkey", r.Form.Get("key"))
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	provider := newTestEasyPay(t, source.URL)
	provider.config["pkey"] = "secret-pkey"
	client := source.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	provider.httpClient = client

	_, err := provider.QueryOrder(context.Background(), "order-1")
	require.Error(t, err)
	require.Zero(t, redirectedCalls.Load(), "EasyPay must never follow redirects carrying the merchant secret")
}
