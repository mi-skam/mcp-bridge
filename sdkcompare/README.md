# sdkcompare

Acceptance harness for the mcp-go → go-sdk migration. Drives both SDKs
through one interface (`Client`) and runs identical scenarios. Separate Go
module so the bridge itself keeps a single MCP dependency.

```sh
cd sdkcompare
go test -v ./                                  # offline: fixture stdio server

# live OAuth against a real server; STATE is a *copy* of a bridge credential file
export SDKCOMPARE_URL=https://<host>/mcp-server/http
export SDKCOMPARE_STATE=/tmp/state.json
go test -v -run A2_A3 ./                       # stored credentials, silent refresh
SDKCOMPARE_DEAD=1 go test -v -run A4 ./        # purged registration → auth required, no browser
SDKCOMPARE_INTERACTIVE=1 SDKCOMPARE_FIRST=go-sdk go test -v -run A5 ./   # browser flow, then mcp-go loads the file
SDKCOMPARE_INTERACTIVE=1 SDKCOMPARE_FIRST=mcp-go go test -v -run A5 ./   # and the reverse
```

## Results 2026-09-17 (mcp-go v0.55.1, go-sdk v1.8.0, n8n MCP OAuth server)

| # | Scenario | mcp-go | go-sdk | Note |
|---|---|---|---|---|
| A1 | stdio `tools/list` equality | pass | pass | after normalisation, see below |
| A2 | http connect, valid stored token | pass | pass | |
| A3 | expired token, valid refresh token | pass, silent refresh | pass, silent refresh | go-sdk persists refreshed token via `NewTokenSource` wrapper |
| A4 | expired token, purged client registration | `no valid token available, authorization required` | `oauth2: "server_error" "Internal Server Error"` | go-sdk surfaces the AS's raw refresh error; harness must map it to auth-required |
| A5 | fresh browser auth → other SDK loads file | pass both directions | pass both directions | credential file format compatible; go-sdk adds `token_endpoint` |
| A6 | text / image / structured results | pass | pass | go-sdk `ImageContent.Data` is raw bytes → re-encode base64 for zot |
| A7 | request timeout 500 ms | 501 ms | 501 ms | |
| A8 | server dies mid-call | `transport closed`, no leak | `EOF`, no leak | |
| P1 | stdio connect median ×20 | 5.3 ms | 4.9 ms | |
| P2 | `tools/list` median ×20 | 146 µs | 193 µs | in-process fixture; noise-level |

### Findings that shape the migration

1. **Schema fidelity.** mcp-go decodes `inputSchema` into a typed struct: it
   always emits `"properties":{}` and `"required":[]`, and drops `$schema`
   and unknown keys. go-sdk keeps the wire schema verbatim. Migrating changes
   cached tool fingerprints once; after that the cache is more faithful.
2. **SSRF guard.** go-sdk refuses OAuth metadata discovery against private IPs
   unless the `http.Client` carries a Transport with its own `DialContext`.
   VPN-hosted MCP servers (10.x) are the normal case here; the bridge must
   opt out explicitly (`oauthHTTPClient()`).
3. **`CommandTransport` and ctx.** Building the `exec.Cmd` with
   `exec.CommandContext(connectCtx, …)` kills the server when the connect
   call returns. Use `exec.Command`; the session owns the process lifetime.
4. **Error classification.** mcp-go has `IsOAuthAuthorizationRequiredError`.
   go-sdk returns the AS's `oauth2.RetrieveError`; the bridge must treat any
   refresh failure with stored credentials as "auth required" itself.
5. **Interactive flow shape.** go-sdk runs the whole browser round-trip inside
   `client.Connect` via `AuthorizationCodeFetcher`. The v1.2.1 async
   `/mcp auth` goroutine already fits; the fetcher just returns
   `ErrAuthRequired` when no interactive flow was requested.
6. **Registration reuse.** go-sdk tries `PreregisteredClient` before DCR, so a
   stored `client_id` is reused instead of re-registering on every login.
7. **Image content.** go-sdk `ImageContent.Data []byte` holds decoded bytes;
   zot's `ext.Image` wants base64. One `base64.StdEncoding.EncodeToString`.

### Verdict

go-sdk covers every scenario mcp-go does, with equal or better behaviour, and
the credential store carries over without re-authentication. Migration is a
go.
