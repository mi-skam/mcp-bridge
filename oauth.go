// oauth.go — OAuth 2.1 for HTTP transports on top of go-sdk's auth package.
//
// go-sdk owns discovery (PRM → AS metadata, 2025-03-26 fallback), client
// registration (stored client first, DCR otherwise), PKCE, code exchange,
// refresh, and step-up scopes. This file owns three things:
//
//  1. the credential file ($ZOT_HOME/mcp-oauth/<sha256(url)>.json, 0600),
//     format-compatible with the mcp-go era so upgrades keep their logins;
//  2. the loopback callback listener that turns a browser redirect into a code;
//  3. the policy that a browser only opens during an explicit /mcp auth.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// errAuthRequired means the server demands authorization and no interactive
// flow is running. Surfaces as LOGIN REQUIRED in status.
var errAuthRequired = errors.New("authorization required: run /mcp auth")

// Credentials are scoped to the exact resource URL, never to a display name.
type oauthStore struct{ path string }

// oauthToken mirrors the mcp-go transport.Token JSON so old files still load.
type oauthToken struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresIn    int64     `json:"expires_in,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
}

type oauthCredentials struct {
	ClientID      string      `json:"client_id"`
	ClientSecret  string      `json:"client_secret,omitempty"`
	TokenEndpoint string      `json:"token_endpoint,omitempty"` // absent in mcp-go era files
	Token         *oauthToken `json:"token,omitempty"`
}

func oauthStoreFor(resource string) oauthStore {
	return oauthStore{filepath.Join(zotHome(), "mcp-oauth", fmt.Sprintf("%x.json", sha256.Sum256([]byte(resource))))}
}

func (s oauthStore) read() (oauthCredentials, error) {
	var c oauthCredentials
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if json.Unmarshal(data, &c) != nil {
		return c, errors.New("invalid OAuth credential file")
	}
	return c, nil
}

func (s oauthStore) save(c oauthCredentials) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return errors.New("cannot encode OAuth credentials")
	}
	return writeFileAtomic(s.path, data, 0o600)
}

// oauthHTTPClient opts out of go-sdk's SSRF guard on discovery. go-sdk refuses
// private/reserved IPs unless the Transport has its own DialContext; MCP
// servers on a VPN are the normal case for this bridge.
func oauthHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &http.Client{Timeout: 30 * time.Second, Transport: t}
}

// oauthSession is the per-connection handler state. interactive is set only
// by /mcp auth; every other connect fails fast with errAuthRequired instead
// of opening a browser.
type oauthSession struct {
	store       oauthStore
	resourceURL string
	hc          *http.Client
	interactive bool
	showURL     func(string)

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	redirect string
	codes    chan callback
}

type callback struct{ code, state, iss string }

// newOAuthSession prepares a handler. It returns nil when the resource has no
// stored credentials and no interactive flow was requested: the plain request
// then hits 401 and the caller reports LOGIN REQUIRED without any OAuth cost.
func (s *managedServer) newOAuthSession(interactive bool, showURL func(string)) (*oauthSession, error) {
	store := oauthStoreFor(s.config.URL)
	c, err := store.read()
	if err != nil {
		return nil, err
	}
	if c.ClientID == "" && !interactive {
		return nil, nil
	}
	return &oauthSession{store: store, resourceURL: s.config.URL, hc: oauthHTTPClient(), interactive: interactive, showURL: showURL}, nil
}

// handler builds the go-sdk OAuthHandler for one connection.
func (o *oauthSession) handler() (auth.OAuthHandler, error) {
	stored, err := o.store.read()
	if err != nil {
		return nil, err
	}
	if o.interactive {
		if err := o.listen(); err != nil {
			return nil, err
		}
	}
	// Redirect must be fixed before registration. Non-interactive sessions
	// never reach the browser step, so a placeholder loopback URL is fine.
	redirect := o.redirect
	if redirect == "" {
		redirect = "http://127.0.0.1/callback"
	}
	cfg := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirect,
		Client:      o.hc,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName:              "mcp-bridge",
			RedirectURIs:            []string{redirect},
			TokenEndpointAuthMethod: "none",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
		}},
		RequestRefreshToken:      true,
		AuthorizationCodeFetcher: o.fetchCode,
		NewTokenSource: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			ts := o.persisting(cfg, cfg.TokenSource(ctx, tok))
			// Persist the freshly exchanged token now; login() closes the
			// session before any request would pull it through the wrapper.
			_, err := ts.Token()
			return ts, err
		},
	}
	// A stored registration is reused (go-sdk tries preregistered before DCR)
	// unless this is a fresh interactive login: then a new public client is
	// registered so a purged server-side registration cannot poison the flow.
	if stored.ClientID != "" && !o.interactive {
		cfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: stored.ClientID}
		if stored.ClientSecret != "" {
			cfg.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: stored.ClientSecret}
		}
	}
	if stored.Token != nil && stored.ClientID != "" && !o.interactive {
		ts, err := o.initialTokenSource(stored)
		if err != nil {
			return nil, err
		}
		cfg.InitialTokenSource = ts
	}
	return auth.NewAuthorizationCodeHandler(cfg)
}

// listen starts the one-shot loopback callback server.
func (o *oauthSession) listen() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.listener != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	o.listener = ln
	o.redirect = "http://" + ln.Addr().String() + "/callback"
	o.codes = make(chan callback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			http.Error(w, "OAuth callback requires GET", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		select {
		case o.codes <- callback{q.Get("code"), q.Get("state"), q.Get("iss")}:
			fmt.Fprint(w, "Authorization received. Return to zot.")
		default:
			http.Error(w, "Callback already received", http.StatusConflict)
		}
	})
	o.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go o.server.Serve(ln)
	return nil
}

// close releases the callback listener.
func (o *oauthSession) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.server != nil {
		o.server.Close()
		o.server = nil
	}
	if o.listener != nil {
		o.listener.Close()
		o.listener = nil
	}
}

// fetchCode is go-sdk's hook for the browser step.
func (o *oauthSession) fetchCode(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	if !o.interactive {
		return nil, errAuthRequired
	}
	u, err := url.Parse(args.URL)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("OAuth authorization endpoint must use HTTPS")
	}
	expectState := u.Query().Get("state")
	o.showURL(args.URL)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case cb := <-o.codes:
		if expectState != "" && cb.state != expectState {
			return nil, errors.New("OAuth state mismatch: this callback does not belong to the active login")
		}
		if cb.code == "" {
			return nil, errors.New("authorization declined or missing code")
		}
		return &auth.AuthorizationResult{Code: cb.code, State: cb.state, Iss: cb.iss}, nil
	}
}

// initialTokenSource rebuilds a refreshing token source from the file. Files
// from the mcp-go era lack token_endpoint; discover it the way mcp-go did
// (AS = resource origin, 2025-03-26 fallback).
func (o *oauthSession) initialTokenSource(c oauthCredentials) (oauth2.TokenSource, error) {
	endpoint := c.TokenEndpoint
	if endpoint == "" {
		u, err := url.Parse(o.resourceURL)
		if err != nil {
			return nil, err
		}
		u.Path, u.RawQuery = "", ""
		meta, err := auth.GetAuthServerMetadata(context.Background(), u.String(), o.hc)
		if err != nil {
			return nil, fmt.Errorf("discover token endpoint: %w", err)
		}
		if meta == nil {
			endpoint = u.String() + "/token"
		} else {
			endpoint = meta.TokenEndpoint
		}
	}
	cfg := &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: endpoint, AuthStyle: oauth2.AuthStyleInParams}}
	tok := &oauth2.Token{AccessToken: c.Token.AccessToken, RefreshToken: c.Token.RefreshToken, TokenType: c.Token.TokenType, Expiry: c.Token.ExpiresAt}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, o.hc)
	return o.persisting(cfg, cfg.TokenSource(ctx, tok)), nil
}

// persisting writes every new token (initial or refreshed) to the store.
func (o *oauthSession) persisting(cfg *oauth2.Config, inner oauth2.TokenSource) oauth2.TokenSource {
	return &persistingSource{store: o.store, cfg: cfg, inner: inner}
}

type persistingSource struct {
	store oauthStore
	cfg   *oauth2.Config
	inner oauth2.TokenSource
	mu    sync.Mutex
	last  string
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	tok, err := p.inner.Token()
	if err != nil {
		// Stored credentials exist but cannot be turned into a token: purged
		// registration, revoked grant, or unreachable AS. All need /mcp auth.
		return nil, fmt.Errorf("%w (%v)", errAuthRequired, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if tok.AccessToken == p.last {
		return tok, nil
	}
	p.last = tok.AccessToken
	c, _ := p.store.read()
	c.ClientID, c.ClientSecret, c.TokenEndpoint = p.cfg.ClientID, p.cfg.ClientSecret, p.cfg.Endpoint.TokenURL
	c.Token = &oauthToken{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, ExpiresAt: tok.Expiry}
	if !tok.Expiry.IsZero() {
		c.Token.ExpiresIn = int64(time.Until(tok.Expiry).Seconds())
	}
	return tok, p.store.save(c)
}

// login runs the explicit browser flow: connect with an interactive session,
// which makes go-sdk register, open the browser, wait for the callback and
// exchange the code. Credentials land in the store through persistingSource.
func (s *managedServer) login(ctx context.Context, showURL func(string)) error {
	if s.config.Transport != "streamable-http" && s.config.Transport != "sse" {
		return errors.New("OAuth login requires an HTTP transport")
	}
	u, err := url.Parse(s.config.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("OAuth login requires an HTTPS resource URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	sess, err := s.newOAuthSession(true, showURL)
	if err != nil {
		return err
	}
	defer sess.close()
	c, _, err := s.connect(ctx, sess)
	if err != nil {
		return err
	}
	c.Close()
	return nil
}
