package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
)

type testAuthHandler struct {
	authorized bool
	attempts   int
}

func (h *testAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	if !h.authorized {
		return nil, nil
	}
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "synthetic", TokenType: "Bearer"}), nil
}
func (h *testAuthHandler) Authorize(_ context.Context, _ *http.Request, r *http.Response) error {
	r.Body.Close()
	h.authorized = true
	h.attempts++
	return nil
}
func TestSSEAuthorizationRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	handler := &testAuthHandler{}
	client := oauthRoundTripper(server.Client(), handler)
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || calls != 2 || handler.attempts != 1 {
		t.Fatalf("status=%d calls=%d auth=%d", resp.StatusCode, calls, handler.attempts)
	}
}
func TestOAuthConfig(t *testing.T) {
	for _, raw := range []string{`true`, `false`, `{"clientId":"id","clientSecret":"secret","scope":"read","redirectUri":"http://127.0.0.1:33418/callback"}`} {
		var o OAuthOptions
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			t.Fatal(err)
		}
		if err := o.validate(); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		var back OAuthOptions
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if back != o {
			t.Fatal("roundtrip lost fields")
		}
	}
	for _, o := range []OAuthOptions{{ClientSecret: "secret"}, {RedirectURI: "http://0.0.0.0:33418/callback"}, {RedirectURI: "https://example.test/callback"}} {
		if o.validate() == nil {
			t.Fatal("unsafe options accepted")
		}
	}
	t.Setenv("CLIENT_ID", "configured")
	cfg := ServerConfig{OAuth: &OAuthOptions{ClientID: "${CLIENT_ID}"}}
	if err := expandServerEnv(&cfg, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.OAuth.ClientID != "configured" {
		t.Fatal("OAuth env expansion failed")
	}
}
