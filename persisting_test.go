package main
import ("testing";"golang.org/x/oauth2";"time")
type staticTS struct{ t *oauth2.Token }
func (s staticTS) Token() (*oauth2.Token, error) { return s.t, nil }
func TestPersistingWritesEndpoint(t *testing.T) {
	t.Setenv("ZOT_HOME", t.TempDir())
	st := oauthStoreFor("https://x.test/mcp")
	cfg := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: "https://x.test/token"}}
	p := &persistingSource{store: st, cfg: cfg, inner: staticTS{&oauth2.Token{AccessToken: "a", RefreshToken: "r", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}}}
	if _, err := p.Token(); err != nil { t.Fatal(err) }
	c, _ := st.read()
	if c.TokenEndpoint != "https://x.test/token" || c.ClientID != "cid" || c.Token.RefreshToken != "r" {
		t.Fatalf("incomplete record: %+v", c)
	}
}
