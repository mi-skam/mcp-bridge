// Package sdkcompare drives mark3labs/mcp-go and modelcontextprotocol/go-sdk
// through one minimal interface so the same scenarios run against both.
package sdkcompare

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"

	m3client "github.com/mark3labs/mcp-go/client"
	m3transport "github.com/mark3labs/mcp-go/client/transport"
	m3 "github.com/mark3labs/mcp-go/mcp"
	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool is the SDK-neutral tool definition used for equality checks.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	ReadOnly    bool            `json:"readOnly"`
}

// Block is an SDK-neutral content block.
type Block struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	MIME string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
}

// Result is an SDK-neutral tool result.
type Result struct {
	Blocks     []Block         `json:"blocks"`
	Structured json.RawMessage `json:"structured,omitempty"`
	IsError    bool            `json:"isError"`
}

// Client is the surface the bridge actually needs from an MCP SDK.
type Client interface {
	Name() string
	Connect(ctx context.Context) error
	ListTools(ctx context.Context) ([]Tool, error)
	Call(ctx context.Context, tool string, args map[string]any) (Result, error)
	Close() error
}

// Target describes what to connect to. Exactly one of Command or URL is set.
type Target struct {
	Command string
	Args    []string
	URL     string
	Headers map[string]string
	// OAuth, when non-nil, enables the bearer flow for URL targets.
	OAuth *OAuthOptions
}

// ErrAuthRequired is returned when a server demands authorization and no
// interactive flow is permitted.
var ErrAuthRequired = errors.New("authorization required")

// ---------------------------------------------------------------- mcp-go

type m3Client struct {
	t Target
	c *m3client.Client
}

// NewMCPGo returns a mark3labs/mcp-go backed client.
func NewMCPGo(t Target) Client { return &m3Client{t: t} }

func (c *m3Client) Name() string { return "mcp-go" }

func (c *m3Client) Connect(ctx context.Context) error {
	var err error
	switch {
	case c.t.Command != "":
		c.c, err = m3client.NewStdioMCPClientWithOptions(c.t.Command, nil, c.t.Args,
			m3transport.WithCommandFunc(func(_ context.Context, command string, env, args []string) (*exec.Cmd, error) {
				cmd := exec.Command(command, args...)
				cmd.Env = append(os.Environ(), env...)
				return cmd, nil
			}))
	default:
		headers := map[string]string{"Accept": "application/json, text/event-stream"}
		for k, v := range c.t.Headers {
			headers[k] = v
		}
		opts := []m3transport.StreamableHTTPCOption{m3transport.WithHTTPHeaders(headers)}
		if c.t.OAuth != nil {
			cfg, oerr := c.t.OAuth.mcpGoConfig()
			if oerr != nil {
				return oerr
			}
			if cfg != nil {
				opts = append(opts, m3transport.WithHTTPOAuth(*cfg))
			}
		}
		c.c, err = m3client.NewStreamableHttpClient(c.t.URL, opts...)
		if err == nil {
			err = c.c.GetTransport().Start(ctx)
		}
	}
	if err != nil {
		return err
	}
	_, err = c.c.Initialize(ctx, m3.InitializeRequest{Params: m3.InitializeParams{
		ProtocolVersion: m3.LATEST_PROTOCOL_VERSION,
		ClientInfo:      m3.Implementation{Name: "sdkcompare", Version: "0"},
	}})
	if err != nil && m3client.IsOAuthAuthorizationRequiredError(err) {
		return fmt.Errorf("%w: %v", ErrAuthRequired, err)
	}
	return err
}

func (c *m3Client) ListTools(ctx context.Context) ([]Tool, error) {
	res, err := c.c.ListTools(ctx, m3.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema, _ := json.Marshal(t.InputSchema)
		out = append(out, Tool{Name: t.Name, Description: t.Description, Schema: normalize(schema), ReadOnly: t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint})
	}
	sortTools(out)
	return out, nil
}

func (c *m3Client) Call(ctx context.Context, tool string, args map[string]any) (Result, error) {
	req := m3.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := c.c.CallTool(ctx, req)
	if err != nil {
		return Result{}, err
	}
	r := Result{IsError: res.IsError}
	for _, b := range res.Content {
		switch v := b.(type) {
		case m3.TextContent:
			r.Blocks = append(r.Blocks, Block{Type: "text", Text: v.Text})
		case m3.ImageContent:
			r.Blocks = append(r.Blocks, Block{Type: "image", MIME: v.MIMEType, Data: v.Data})
		default:
			data, _ := json.Marshal(v)
			r.Blocks = append(r.Blocks, Block{Type: "other", Text: string(data)})
		}
	}
	if res.StructuredContent != nil {
		sc, _ := json.Marshal(res.StructuredContent)
		r.Structured = normalize(sc)
	}
	return r, nil
}

func (c *m3Client) Close() error {
	if c.c == nil {
		return nil
	}
	return c.c.Close()
}

// ---------------------------------------------------------------- go-sdk

type gosdkClient struct {
	t  Target
	cs *gosdk.ClientSession
}

// NewGoSDK returns a modelcontextprotocol/go-sdk backed client.
func NewGoSDK(t Target) Client { return &gosdkClient{t: t} }

func (c *gosdkClient) Name() string { return "go-sdk" }

func (c *gosdkClient) Connect(ctx context.Context) error {
	var transport gosdk.Transport
	switch {
	case c.t.Command != "":
		// Process lifetime is the session's, not the connect call's: never bind it to ctx.
		cmd := exec.Command(c.t.Command, c.t.Args...)
		cmd.Env = os.Environ()
		transport = &gosdk.CommandTransport{Command: cmd}
	default:
		st := &gosdk.StreamableClientTransport{Endpoint: c.t.URL, HTTPClient: headerClient(c.t.Headers)}
		if c.t.OAuth != nil {
			h, err := c.t.OAuth.goSDKHandler()
			if err != nil {
				return err
			}
			st.OAuthHandler = h
		}
		transport = st
	}
	client := gosdk.NewClient(&gosdk.Implementation{Name: "sdkcompare", Version: "0"}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	c.cs = cs
	return nil
}

func (c *gosdkClient) ListTools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	var cursor string
	for {
		res, err := c.cs.ListTools(ctx, &gosdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, t := range res.Tools {
			schema, _ := json.Marshal(t.InputSchema)
			out = append(out, Tool{Name: t.Name, Description: t.Description, Schema: normalize(schema), ReadOnly: t.Annotations != nil && t.Annotations.ReadOnlyHint})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	sortTools(out)
	return out, nil
}

func (c *gosdkClient) Call(ctx context.Context, tool string, args map[string]any) (Result, error) {
	res, err := c.cs.CallTool(ctx, &gosdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return Result{}, err
	}
	r := Result{IsError: res.IsError}
	for _, b := range res.Content {
		switch v := b.(type) {
		case *gosdk.TextContent:
			r.Blocks = append(r.Blocks, Block{Type: "text", Text: v.Text})
		case *gosdk.ImageContent:
			// go-sdk decodes wire base64 into raw bytes; zot wants base64 back.
			r.Blocks = append(r.Blocks, Block{Type: "image", MIME: v.MIMEType, Data: base64.StdEncoding.EncodeToString(v.Data)})
		default:
			data, _ := json.Marshal(v)
			r.Blocks = append(r.Blocks, Block{Type: "other", Text: string(data)})
		}
	}
	if res.StructuredContent != nil {
		sc, _ := json.Marshal(res.StructuredContent)
		r.Structured = normalize(sc)
	}
	return r, nil
}

func (c *gosdkClient) Close() error {
	if c.cs == nil {
		return nil
	}
	return c.cs.Close()
}

// ---------------------------------------------------------------- helpers

// normalize re-marshals JSON with sorted keys and drops empty
// properties/required so byte comparison is meaningful.
//
// Findings (mcp-go typed ToolInputSchema vs go-sdk verbatim wire schema):
//   - mcp-go always emits "properties":{} and "required":[] even when absent
//   - mcp-go drops "$schema" and any top-level key it has no field for
//
// go-sdk is the faithful one; both are semantically equal for tool calling.
func normalize(raw []byte) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	if m, ok := v.(map[string]any); ok {
		if p, ok := m["properties"].(map[string]any); ok && len(p) == 0 {
			delete(m, "properties")
		}
		if r, ok := m["required"].([]any); ok && len(r) == 0 {
			delete(m, "required")
		}
		delete(m, "$schema")
	}
	out, _ := json.Marshal(v) // encoding/json sorts map keys
	return out
}

func sortTools(ts []Tool) { sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name }) }

// Timed runs fn and returns its duration.
func Timed(fn func() error) (time.Duration, error) {
	start := time.Now()
	err := fn()
	return time.Since(start), err
}
