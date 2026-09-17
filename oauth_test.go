package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestOAuthStore(t *testing.T) {
	t.Setenv("ZOT_HOME", t.TempDir())
	s := oauthStoreFor("https://example.test/mcp")
	if c, err := s.read(); err != nil || c.ClientID != "" || c.Token != nil {
		t.Fatalf("missing file must read as empty: %+v %v", c, err)
	}
	if s.path == oauthStoreFor("https://other.test/mcp").path {
		t.Fatal("resource collision")
	}
	if err := s.save(oauthCredentials{ClientID: "synthetic-client", Token: &oauthToken{AccessToken: "synthetic-token", RefreshToken: "synthetic-refresh"}}); err != nil {
		t.Fatal(err)
	}
	c, err := s.read()
	if err != nil || c.ClientID != "synthetic-client" || c.Token.RefreshToken != "synthetic-refresh" {
		t.Fatal("credentials did not round trip")
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("insecure token permissions")
	}
	if err := os.WriteFile(s.path, []byte("synthetic-secret-invalid-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.read(); err == nil || err.Error() != "invalid OAuth credential file" {
		t.Fatal("unsafe corruption diagnostic")
	}
}

// Files written by the mcp-go era (v1.x) must load unchanged: same keys,
// RFC3339 expires_at, no token_endpoint.
func TestOAuthStoreLoadsV1File(t *testing.T) {
	t.Setenv("ZOT_HOME", t.TempDir())
	s := oauthStoreFor("https://example.test/mcp")
	os.MkdirAll(filepath.Dir(s.path), 0o700)
	v1 := `{"client_id":"c9b4af58","token":{"access_token":"a","token_type":"Bearer","refresh_token":"r","expires_in":3600,"expires_at":"2026-09-16T18:10:55.480443+02:00"}}`
	if err := os.WriteFile(s.path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	if c.ClientID != "c9b4af58" || c.Token.RefreshToken != "r" || c.TokenEndpoint != "" {
		t.Fatalf("v1 file misread: %+v", c)
	}
	want := time.Date(2026, 9, 16, 16, 10, 55, 480443000, time.UTC)
	if !c.Token.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", c.Token.ExpiresAt, want)
	}
}

// Background connects never open a browser: without an interactive session
// a stored-credential-less server gets no OAuth handler at all, and a
// handler built non-interactively refuses the browser step.
func TestOAuthNonInteractiveNeverOpensBrowser(t *testing.T) {
	t.Setenv("ZOT_HOME", t.TempDir())
	s := &managedServer{config: ServerConfig{Transport: "streamable-http", URL: "https://example.test/mcp"}}
	sess, err := s.newOAuthSession(false, nil)
	if err != nil || sess != nil {
		t.Fatalf("no credentials + non-interactive must yield no session: %v %v", sess, err)
	}
	sess = &oauthSession{store: oauthStoreFor(s.config.URL), resourceURL: s.config.URL, hc: oauthHTTPClient()}
	if _, err := sess.fetchCode(context.Background(), nil); err != errAuthRequired {
		t.Fatalf("non-interactive fetchCode = %v, want errAuthRequired", err)
	}
}

func TestOAuthLoginRejectsInsecureResource(t *testing.T) {
	s := &managedServer{config: ServerConfig{Transport: "streamable-http", URL: "http://example.test/mcp"}}
	if err := s.login(context.Background(), func(string) { t.Fatal("unexpected authorization") }); err == nil {
		t.Fatal("accepted insecure resource")
	}
}
