package sdkcompare

import (
	"context"
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

	m3transport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// OAuthOptions configures the bearer flow for one resource URL.
type OAuthOptions struct {
	// StatePath is the credential file. Format matches the bridge's
	// $ZOT_HOME/mcp-oauth/<sha256(url)>.json so both SDKs can load each other's output.
	StatePath string
	// Interactive permits opening a browser (via Open) and waiting for the
	// loopback callback. When false any authorization demand fails with ErrAuthRequired.
	Interactive bool
	// Open receives the authorization URL. nil prints to stderr.
	Open func(url string)
	// ResourceURL is the MCP endpoint; needed for AS discovery when the stored
	// file predates the token_endpoint field.
	ResourceURL string
}

// credentials is the on-disk shape. TokenEndpoint is new (go-sdk needs it to
// refresh without rediscovery); older mcp-go files omit it.
type credentials struct {
	ClientID      string             `json:"client_id"`
	ClientSecret  string             `json:"client_secret,omitempty"`
	TokenEndpoint string             `json:"token_endpoint,omitempty"`
	Token         *m3transport.Token `json:"token,omitempty"`
}

func (o *OAuthOptions) read() (credentials, error) {
	var c credentials
	data, err := os.ReadFile(o.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(data, &c)
}

func (o *OAuthOptions) write(c credentials) error {
	if err := os.MkdirAll(filepath.Dir(o.StatePath), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := o.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, o.StatePath)
}

func (o *OAuthOptions) open(u string) {
	if o.Open != nil {
		o.Open(u)
		return
	}
	fmt.Fprintln(os.Stderr, "open in browser:", u)
}

// loopback starts a one-shot callback listener and returns its redirect URL
// plus a function that waits for the code (validating state when expected != "").
func loopback(ctx context.Context) (redirect string, wait func(expectState string) (code, state, iss string, err error), stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	type cb struct{ code, state, iss string }
	got := make(chan cb, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		select {
		case got <- cb{q.Get("code"), q.Get("state"), q.Get("iss")}:
			fmt.Fprint(w, "Authorization received. Return to the terminal.")
		default:
			http.Error(w, "callback already received", http.StatusConflict)
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	redirect = "http://" + ln.Addr().String() + "/callback"
	wait = func(expectState string) (string, string, string, error) {
		select {
		case <-ctx.Done():
			return "", "", "", ctx.Err()
		case c := <-got:
			if expectState != "" && c.state != expectState {
				return "", "", "", errors.New("oauth state mismatch")
			}
			if c.code == "" {
				return "", "", "", errors.New("authorization declined")
			}
			return c.code, c.state, c.iss, nil
		}
	}
	return redirect, wait, func() { srv.Close(); ln.Close() }, nil
}

// ---------------------------------------------------------------- mcp-go

// mcpGoConfig mirrors the bridge's oauth.go: stored client → OAuthConfig with
// a file TokenStore; no stored client → nil (plain request, 401 surfaces).
// Interactive login for mcp-go is a separate manual flow (LoginMCPGo).
func (o *OAuthOptions) mcpGoConfig() (*m3transport.OAuthConfig, error) {
	c, err := o.read()
	if err != nil {
		return nil, err
	}
	if c.ClientID == "" {
		return nil, nil
	}
	return &m3transport.OAuthConfig{ClientID: c.ClientID, ClientSecret: c.ClientSecret, TokenStore: m3store{o}, PKCEEnabled: true}, nil
}

type m3store struct{ o *OAuthOptions }

func (s m3store) GetToken(ctx context.Context) (*m3transport.Token, error) {
	c, err := s.o.read()
	if err != nil {
		return nil, err
	}
	if c.Token == nil {
		return nil, m3transport.ErrNoToken
	}
	return c.Token, nil
}

func (s m3store) SaveToken(ctx context.Context, t *m3transport.Token) error {
	c, err := s.o.read()
	if err != nil {
		return err
	}
	c.Token = t
	return s.o.write(c)
}

// LoginMCPGo runs the bridge's explicit browser flow with mcp-go primitives.
func (o *OAuthOptions) LoginMCPGo(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	redirect, wait, stop, err := loopback(ctx)
	if err != nil {
		return err
	}
	defer stop()
	state, _ := m3transport.GenerateState()
	verifier, _ := m3transport.GenerateCodeVerifier()
	hc := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, o.ResourceURL, nil)
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	mem := m3transport.NewMemoryTokenStore()
	h := m3transport.NewOAuthHandler(m3transport.OAuthConfig{RedirectURI: redirect, PKCEEnabled: true, TokenStore: mem, HTTPClient: hc})
	h.SetBaseURL(o.ResourceURL)
	h.HandleUnauthorizedResponse(resp)
	if err := h.RegisterClient(ctx, "sdkcompare"); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	h.SetExpectedState(state)
	authURL, err := h.GetAuthorizationURL(ctx, state, m3transport.GenerateCodeChallenge(verifier))
	if err != nil {
		return err
	}
	o.open(authURL)
	code, _, _, err := wait(state)
	if err != nil {
		return err
	}
	if err := h.ProcessAuthorizationResponse(ctx, code, state, verifier); err != nil {
		return fmt.Errorf("exchange: %w", err)
	}
	tok, err := mem.GetToken(ctx)
	if err != nil {
		return err
	}
	return o.write(credentials{ClientID: h.GetClientID(), ClientSecret: h.GetClientSecret(), Token: tok})
}

// ---------------------------------------------------------------- go-sdk

// goSDKHandler builds an auth.OAuthHandler that (a) starts from stored
// credentials when present, (b) persists every refreshed token, and (c) only
// opens a browser when Interactive is set.
func (o *OAuthOptions) goSDKHandler() (auth.OAuthHandler, error) {
	stored, err := o.read()
	if err != nil {
		return nil, err
	}
	hc := oauthHTTPClient()
	var pending struct {
		sync.Mutex
		redirect string
		wait     func(string) (string, string, string, error)
		stop     func()
	}
	// Registration happens inside Authorize; the redirect must exist before
	// that, so the listener is created here and reused for the single flow.
	redirect, wait, stop, err := loopback(context.Background())
	if err != nil {
		return nil, err
	}
	pending.redirect, pending.wait, pending.stop = redirect, wait, stop

	cfg := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirect,
		Client:      hc,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName:              "sdkcompare",
			RedirectURIs:            []string{redirect},
			TokenEndpointAuthMethod: "none",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
		}},
		RequestRefreshToken: true,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			if !o.Interactive {
				return nil, ErrAuthRequired
			}
			o.open(args.URL)
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			u, _ := url.Parse(args.URL)
			code, state, iss, err := pending.wait(u.Query().Get("state"))
			if err != nil {
				return nil, err
			}
			return &auth.AuthorizationResult{Code: code, State: state, Iss: iss}, nil
		},
		NewTokenSource: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			return o.persisting(cfg, cfg.TokenSource(ctx, tok)), nil
		},
	}
	// Stored client registration from a previous run (either SDK): prefer it
	// so a still-valid registration is reused instead of creating another.
	if stored.ClientID != "" {
		cfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: stored.ClientID}
		if stored.ClientSecret != "" {
			cfg.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: stored.ClientSecret}
		}
	}
	if stored.Token != nil && stored.ClientID != "" {
		ts, err := o.initialTokenSource(hc, stored)
		if err != nil {
			return nil, err
		}
		cfg.InitialTokenSource = ts
	}
	return auth.NewAuthorizationCodeHandler(cfg)
}

// initialTokenSource rebuilds a refreshing token source from a stored file.
// Files written by mcp-go lack token_endpoint; discover it via the 2025-03-26
// fallback (AS = resource origin), which is what mcp-go itself does.
func (o *OAuthOptions) initialTokenSource(hc *http.Client, c credentials) (oauth2.TokenSource, error) {
	endpoint := c.TokenEndpoint
	if endpoint == "" {
		u, err := url.Parse(o.ResourceURL)
		if err != nil {
			return nil, err
		}
		u.Path, u.RawQuery = "", ""
		meta, err := auth.GetAuthServerMetadata(context.Background(), u.String(), hc)
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
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, hc)
	return o.persisting(cfg, cfg.TokenSource(ctx, tok)), nil
}

// persisting wraps a token source and writes every new token to disk.
func (o *OAuthOptions) persisting(cfg *oauth2.Config, inner oauth2.TokenSource) oauth2.TokenSource {
	return &persistingSource{o: o, cfg: cfg, inner: inner}
}

type persistingSource struct {
	o     *OAuthOptions
	cfg   *oauth2.Config
	inner oauth2.TokenSource
	mu    sync.Mutex
	last  string
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	tok, err := p.inner.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if tok.AccessToken == p.last {
		return tok, nil
	}
	p.last = tok.AccessToken
	c, _ := p.o.read()
	c.ClientID, c.ClientSecret, c.TokenEndpoint = p.cfg.ClientID, p.cfg.ClientSecret, p.cfg.Endpoint.TokenURL
	c.Token = &m3transport.Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, ExpiresAt: tok.Expiry}
	if !tok.Expiry.IsZero() {
		c.Token.ExpiresIn = int64(time.Until(tok.Expiry).Seconds())
	}
	return tok, p.o.write(c)
}

// oauthHTTPClient opts out of go-sdk's SSRF guard on OAuth discovery.
//
// Finding: go-sdk (oauthex.newDiscoveryTransport) refuses to dial private or
// reserved IPs during metadata discovery unless the caller supplies a
// Transport with its own DialContext. MCP servers on a VPN (10.x) are the
// normal case for this bridge, so the bridge must set this explicitly.
func oauthHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &http.Client{Timeout: 30 * time.Second, Transport: t}
}

// headerClient returns an http.Client that adds static headers to every request.
func headerClient(h map[string]string) *http.Client {
	if len(h) == 0 {
		return &http.Client{Timeout: 60 * time.Second}
	}
	return &http.Client{Timeout: 60 * time.Second, Transport: headerRT{h, http.DefaultTransport}}
}

type headerRT struct {
	h  map[string]string
	rt http.RoundTripper
}

func (t headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.h {
		r.Header.Set(k, v)
	}
	return t.rt.RoundTrip(r)
}
