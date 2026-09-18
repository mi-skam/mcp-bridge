// protocol.go — fixed tools for the parts of MCP that do not map onto one
// deferred zot tool per MCP tool: generic call/describe (tools discovered after
// startup, no /reload-ext needed), resources, and prompts.
//
// Four small always-on tools instead of N: their schemas are static, so the
// context cost is constant regardless of how many servers are configured.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/patriceckhart/zot/packages/agent/ext"
)

const (
	mcpCallToolName      = "mcp__call"
	mcpDescribeToolName  = "mcp__describe"
	mcpResourcesToolName = "mcp__resources"
	mcpPromptsToolName   = "mcp__prompts"
)

var mcpCallSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "Configured MCP server name, as listed by /mcp list."},
    "tool": {"type": "string", "description": "MCP tool name on that server (not the mcp__ prefixed zot name)."},
    "args": {"type": "object", "additionalProperties": true, "description": "Tool arguments matching the tool's input schema. Use mcp__describe to see it."}
  },
  "required": ["server", "tool"],
  "additionalProperties": false
}`)

var mcpDescribeSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "Configured MCP server name."},
    "tool": {"type": "string", "description": "Tool name to describe. Omit to list every tool on the server with a one-line description."}
  },
  "required": ["server"],
  "additionalProperties": false
}`)

var mcpResourcesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "Configured MCP server name."},
    "action": {"type": "string", "enum": ["list", "read", "subscribe", "unsubscribe"], "description": "List resources/templates, read a URI, or manage session-scoped update subscriptions."},
    "uri": {"type": "string", "description": "Resource URI for read, subscribe or unsubscribe."}
  },
  "required": ["server", "action"],
  "additionalProperties": false
}`)

var mcpPromptsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "Configured MCP server name."},
    "action": {"type": "string", "enum": ["list", "get"], "description": "list: prompt templates with arguments. get: render one prompt."},
    "name": {"type": "string", "description": "Prompt name for get."},
    "args": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Prompt arguments for get (string values)."}
  },
  "required": ["server", "action"],
  "additionalProperties": false
}`)

// registerProtocolTools adds the four fixed tools. Called once at startup next
// to the search tool.
func (b *bridge) registerProtocolTools() {
	b.e.Tool(mcpCallToolName,
		"Call any tool on a configured MCP server by server and tool name, including tools discovered after startup. Prefer the dedicated mcp__<server>__<tool> tool when it is loaded; use mcp__describe first for the argument schema.",
		mcpCallSchema, b.callTool)
	b.e.Tool(mcpDescribeToolName,
		"Show the live tool list of an MCP server, or the full input schema and annotations of one tool.",
		mcpDescribeSchema, b.describeTool)
	b.e.Tool(mcpResourcesToolName,
		"List or read MCP resources (files, documents, records exposed by a server via URI).",
		mcpResourcesSchema, b.resourcesTool)
	b.e.Tool(mcpPromptsToolName,
		"List or render MCP prompt templates offered by a server.",
		mcpPromptsSchema, b.promptsTool)
	b.e.Tool(mcpControlToolName, "Ping an MCP server, set its logging level, or complete a prompt/resource argument.", mcpControlSchema, b.controlTool)
}

func (b *bridge) server(name string) (*managedServer, ext.ToolResult, bool) {
	srv, ok := b.servers[name]
	if !ok {
		names := make([]string, 0, len(b.servers))
		for n := range b.servers {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, ext.TextErrorResult(fmt.Sprintf("unknown MCP server %q. Configured: %s", name, strings.Join(names, ", "))), false
	}
	return srv, ext.ToolResult{}, true
}

func (b *bridge) callTool(raw json.RawMessage) ext.ToolResult {
	var in struct {
		Server string          `json:"server"`
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ext.TextErrorResult(mcpCallToolName + ": invalid arguments: " + err.Error())
	}
	srv, res, ok := b.server(in.Server)
	if !ok {
		return res
	}
	result, err := srv.callTool(context.Background(), in.Tool, in.Args)
	if err != nil {
		return ext.TextErrorResult(fmt.Sprintf("%s/%s: %v", in.Server, in.Tool, err))
	}
	return mcpResultToZot(result)
}

func (b *bridge) describeTool(raw json.RawMessage) ext.ToolResult {
	var in struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ext.TextErrorResult(mcpDescribeToolName + ": invalid arguments: " + err.Error())
	}
	srv, res, ok := b.server(in.Server)
	if !ok {
		return res
	}
	tools, err := srv.liveTools(context.Background())
	if err != nil {
		return ext.TextErrorResult(fmt.Sprintf("%s: %v", in.Server, err))
	}
	if in.Tool == "" {
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s: %d tool(s)\n", in.Server, len(tools))
		for _, t := range tools {
			fmt.Fprintf(&sb, "- %s: %s\n", t.Name, firstLine(mcpToolDescription(in.Server, t)))
		}
		return ext.TextResult(sb.String())
	}
	for _, t := range tools {
		if t.Name != in.Tool {
			continue
		}
		out := map[string]any{
			"server":      in.Server,
			"tool":        t.Name,
			"zotTool":     toolName(in.Server, t.Name),
			"description": mcpToolDescription(in.Server, t),
			"inputSchema": mcpToolSchema(t),
		}
		if t.OutputSchema != nil {
			out["outputSchema"] = t.OutputSchema
		}
		if t.Annotations != nil {
			out["annotations"] = t.Annotations
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		return ext.TextResult(string(data))
	}
	return ext.TextErrorResult(fmt.Sprintf("%s has no tool %q. Call %s with only the server to list them.", in.Server, in.Tool, mcpDescribeToolName))
}

func (b *bridge) resourcesTool(raw json.RawMessage) ext.ToolResult {
	var in struct {
		Server string `json:"server"`
		Action string `json:"action"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ext.TextErrorResult(mcpResourcesToolName + ": invalid arguments: " + err.Error())
	}
	srv, res, ok := b.server(in.Server)
	if !ok {
		return res
	}
	switch in.Action {
	case "subscribe", "unsubscribe":
		if strings.TrimSpace(in.URI) == "" {
			return ext.TextErrorResult(in.Action + " requires uri")
		}
		if err := srv.resourceSubscription(context.Background(), in.URI, in.Action == "subscribe"); err != nil {
			return ext.TextErrorResult(err.Error())
		}
		return ext.TextResult(in.Action + " succeeded: " + in.URI + " (subscriptions end when the connection stops)")
	case "list":
		resources, templates, err := srv.listResources(context.Background())
		if err != nil {
			return ext.TextErrorResult(fmt.Sprintf("%s: %v", in.Server, err))
		}
		if len(resources)+len(templates) == 0 {
			return ext.TextResult(in.Server + " exposes no resources.")
		}
		var sb strings.Builder
		for _, r := range resources {
			fmt.Fprintf(&sb, "- %s", r.URI)
			if r.Name != "" {
				fmt.Fprintf(&sb, " (%s)", r.Name)
			}
			if r.MIMEType != "" {
				fmt.Fprintf(&sb, " [%s]", r.MIMEType)
			}
			if r.Description != "" {
				fmt.Fprintf(&sb, ": %s", firstLine(r.Description))
			}
			sb.WriteByte('\n')
		}
		for _, t := range templates {
			fmt.Fprintf(&sb, "- template %s", t.URITemplate)
			if t.Name != "" {
				fmt.Fprintf(&sb, " (%s)", t.Name)
			}
			if t.Description != "" {
				fmt.Fprintf(&sb, ": %s", firstLine(t.Description))
			}
			sb.WriteByte('\n')
		}
		return ext.TextResult(sb.String())
	case "read":
		if in.URI == "" {
			return ext.TextErrorResult("read requires uri")
		}
		result, err := srv.readResource(context.Background(), in.URI)
		if err != nil {
			return ext.TextErrorResult(fmt.Sprintf("%s: read %s: %v", in.Server, in.URI, err))
		}
		return resourceContentsToZot(result.Contents)
	default:
		return ext.TextErrorResult("action must be list, read, subscribe or unsubscribe")
	}
}

func (b *bridge) promptsTool(raw json.RawMessage) ext.ToolResult {
	var in struct {
		Server string            `json:"server"`
		Action string            `json:"action"`
		Name   string            `json:"name"`
		Args   map[string]string `json:"args"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ext.TextErrorResult(mcpPromptsToolName + ": invalid arguments: " + err.Error())
	}
	srv, res, ok := b.server(in.Server)
	if !ok {
		return res
	}
	switch in.Action {
	case "list":
		prompts, err := srv.listPrompts(context.Background())
		if err != nil {
			return ext.TextErrorResult(fmt.Sprintf("%s: %v", in.Server, err))
		}
		if len(prompts) == 0 {
			return ext.TextResult(in.Server + " exposes no prompts.")
		}
		var sb strings.Builder
		for _, p := range prompts {
			fmt.Fprintf(&sb, "- %s", p.Name)
			if p.Description != "" {
				fmt.Fprintf(&sb, ": %s", firstLine(p.Description))
			}
			for _, a := range p.Arguments {
				req := ""
				if a.Required {
					req = ", required"
				}
				fmt.Fprintf(&sb, "\n    %s%s: %s", a.Name, req, firstLine(a.Description))
			}
			sb.WriteByte('\n')
		}
		return ext.TextResult(sb.String())
	case "get":
		if in.Name == "" {
			return ext.TextErrorResult("get requires name")
		}
		result, err := srv.getPrompt(context.Background(), in.Name, in.Args)
		if err != nil {
			return ext.TextErrorResult(fmt.Sprintf("%s: prompt %s: %v", in.Server, in.Name, err))
		}
		var contents []ext.ToolContent
		if result.Description != "" {
			contents = append(contents, ext.Text(result.Description))
		}
		for _, m := range result.Messages {
			contents = append(contents, promptContentToZot(m)...)
		}
		if len(contents) == 0 {
			contents = append(contents, ext.Text("(empty prompt)"))
		}
		return ext.ToolResult{Content: contents}
	default:
		return ext.TextErrorResult("action must be list or get")
	}
}

// resourceContentsToZot renders text and images natively and preserves other
// binary content as base64 JSON with its URI and MIME type.
func resourceContentsToZot(contents []*mcp.ResourceContents) ext.ToolResult {
	var out []ext.ToolContent
	for _, c := range contents {
		switch {
		case c.Text != "":
			out = append(out, ext.Text(c.Text))
		case len(c.Blob) > 0 && strings.HasPrefix(c.MIMEType, "image/"):
			out = append(out, ext.Image(c.MIMEType, base64.StdEncoding.EncodeToString(c.Blob)))
		case len(c.Blob) > 0:
			data, err := json.Marshal(c)
			if err != nil {
				return ext.TextErrorResult("encode resource: " + err.Error())
			}
			out = append(out, ext.Text(string(data)))
		}
	}
	if len(out) == 0 {
		out = append(out, ext.Text("(empty resource)"))
	}
	return ext.ToolResult{Content: out}
}

func promptContentToZot(m *mcp.PromptMessage) []ext.ToolContent {
	prefix := string(m.Role) + ": "
	switch v := m.Content.(type) {
	case *mcp.TextContent:
		return []ext.ToolContent{ext.Text(prefix + v.Text)}
	case *mcp.ImageContent:
		return []ext.ToolContent{ext.Text(prefix + "(image)"), ext.Image(v.MIMEType, base64.StdEncoding.EncodeToString(v.Data))}
	case *mcp.EmbeddedResource:
		if v.Resource == nil {
			return nil
		}
		r := resourceContentsToZot([]*mcp.ResourceContents{v.Resource})
		return append([]ext.ToolContent{ext.Text(prefix + "(resource " + v.Resource.URI + ")")}, r.Content...)
	default:
		data, _ := json.Marshal(v)
		return []ext.ToolContent{ext.Text(prefix + string(data))}
	}
}
