// fixture is a stdio MCP server with deterministic tools for SDK comparison.
//
// Tools:
//
//	echo{text}          → text content
//	image               → 1x1 PNG image content
//	structured{n}       → structured JSON output + text
//	sleep{ms}           → blocks ms milliseconds, then returns text
//	die                 → os.Exit(3) mid-request (transport failure)
package main

import (
	"context"
	"encoding/base64"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// onePixelPNG is base64 here for readability; the tool sends raw bytes
// because go-sdk ImageContent.Data is []byte and is base64-encoded on the wire.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

type echoIn struct {
	Text string `json:"text"`
}
type sleepIn struct {
	Ms int `json:"ms"`
}
type structuredIn struct {
	N int `json:"n"`
}
type structuredOut struct {
	N       int   `json:"n"`
	Squares []int `json:"squares"`
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "sdkcompare-fixture", Version: "0.0.1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo text back."}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "image", Description: "Return a 1x1 PNG.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: mustB64(onePixelPNG)}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "structured", Description: "Return structured output."}, func(_ context.Context, _ *mcp.CallToolRequest, in structuredIn) (*mcp.CallToolResult, structuredOut, error) {
		out := structuredOut{N: in.N}
		for i := 1; i <= in.N; i++ {
			out.Squares = append(out.Squares, i*i)
		}
		return nil, out, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "sleep", Description: "Sleep ms milliseconds."}, func(ctx context.Context, _ *mcp.CallToolRequest, in sleepIn) (*mcp.CallToolResult, any, error) {
		select {
		case <-time.After(time.Duration(in.Ms) * time.Millisecond):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "slept"}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "die", Description: "Exit the process mid-request."}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		os.Exit(3)
		return nil, nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
