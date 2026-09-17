package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/patriceckhart/zot/packages/agent/ext"
)

const mcpControlToolName = "mcp__control"

var mcpControlSchema = json.RawMessage(`{
 "type":"object",
 "properties":{
  "server":{"type":"string"},
  "action":{"type":"string","enum":["ping","logging/set","complete"]},
  "level":{"type":"string","enum":["debug","info","notice","warning","error","critical","alert","emergency"]},
  "ref":{"type":"object","properties":{"type":{"type":"string","enum":["ref/prompt","ref/resource"]},"name":{"type":"string"},"uri":{"type":"string"}},"required":["type"],"additionalProperties":false},
  "argument":{"type":"object","properties":{"name":{"type":"string"},"value":{"type":"string"}},"required":["name","value"],"additionalProperties":false},
  "context":{"type":"object","properties":{"arguments":{"type":"object","additionalProperties":{"type":"string"}}},"additionalProperties":false}
 },
 "required":["server","action"],"additionalProperties":false
}`)

func (b *bridge) controlTool(raw json.RawMessage) ext.ToolResult {
	var in struct {
		Server   string                      `json:"server"`
		Action   string                      `json:"action"`
		Level    mcp.LoggingLevel            `json:"level"`
		Ref      *mcp.CompleteReference      `json:"ref"`
		Argument *mcp.CompleteParamsArgument `json:"argument"`
		Context  *mcp.CompleteContext        `json:"context"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return ext.TextErrorResult("invalid control arguments: " + err.Error())
	}
	if dec.Decode(new(any)) != io.EOF {
		return ext.TextErrorResult("expected one JSON object")
	}
	switch in.Action {
	case "ping":
	case "logging/set":
		switch in.Level {
		case "debug", "info", "notice", "warning", "error", "critical", "alert", "emergency":
		default:
			return ext.TextErrorResult("logging/set requires a valid level")
		}
	case "complete":
		if in.Ref == nil || in.Argument == nil || in.Argument.Name == "" {
			return ext.TextErrorResult("complete requires ref and argument with a name and value")
		}
		if (in.Ref.Type == "ref/prompt" && in.Ref.Name == "") || (in.Ref.Type == "ref/resource" && in.Ref.URI == "") {
			return ext.TextErrorResult("completion reference requires a prompt name or resource URI")
		}
	default:
		return ext.TextErrorResult("action must be ping, logging/set or complete")
	}
	srv, result, ok := b.server(in.Server)
	if !ok {
		return result
	}
	var out any = map[string]any{"server": in.Server, "action": in.Action, "ok": true}
	err := srv.withSession(context.Background(), func(ctx context.Context, c *mcp.ClientSession) error {
		switch in.Action {
		case "ping":
			return c.Ping(ctx, nil)
		case "logging/set":
			return c.SetLoggingLevel(ctx, &mcp.SetLoggingLevelParams{Level: in.Level})
		default:
			var err error
			out, err = c.Complete(ctx, &mcp.CompleteParams{Ref: in.Ref, Argument: *in.Argument, Context: in.Context})
			return err
		}
	})
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	data, err := json.Marshal(out)
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	return ext.TextResult(string(data))
}
