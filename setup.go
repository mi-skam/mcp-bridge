package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

func installHelp() string {
	return `mcp-bridge install

Usage:
  /mcp install                          Show this help
  /mcp install [options] <name> <commandOrUrl> [args...]

Install any MCP server configuration; no predefined server list is required.
This writes configuration only. It does not download or start a server.

Options:
  -t, --transport <stdio|http|sse>  Transport (default: stdio)
  -e, --env KEY=value             Environment variable for stdio (repeatable)
  -H, --header "Name: value"      HTTP/SSE header (repeatable)
  -s, --scope <local|project|user> Configuration scope (default: local)
      --global                   Alias for --scope user
      --project                  Alias for --scope project

Scopes:
  local    <cwd>/.zot/mcp.json (zot-specific; not automatically private)
  project  <cwd>/.mcp.json (shared MCP configuration)
  user     $ZOT_HOME/mcp.json

Examples:
  /mcp install --transport http sentry https://mcp.sentry.dev/mcp
  /mcp install --transport http api https://example.com/mcp -H "Authorization: Bearer ${API_TOKEN}"
  /mcp install docs -- npx -y @upstash/context7-mcp@latest
  /mcp install worker -e API_KEY=${API_KEY} -- npx my-mcp-server --some-flag
  /mcp install --scope user filesystem -- npx -y @modelcontextprotocol/server-filesystem "/path with spaces"

Quote values containing spaces. Use -- before the executable to pass its flags
verbatim. Shell expansion and shell execution are not performed. Environment
references such as ${API_KEY} are saved literally and resolved when config loads.
Prefer references over literal secrets, especially in project files or chat.
Run /reload-ext after installing; use /mcp auth <name> for browser OAuth.`
}

func handleInstall(args []string, cwd string) (string, error) {
	if len(args) == 0 || (len(args) == 1 && args[0] == "help") {
		return installHelp(), nil
	}

	cfg := ServerConfig{Transport: "stdio"}
	scope := "local"
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		option, inline, hasInline := strings.Cut(arg, "=")
		switch option {
		case "--help", "-h":
			if hasInline {
				return "", fmt.Errorf("%s does not accept a value", option)
			}
			return installHelp(), nil
		case "--global", "--project":
			if hasInline {
				return "", fmt.Errorf("%s does not accept a value", option)
			}
			if option == "--global" {
				scope = "user"
			} else {
				scope = "project"
			}
			continue
		case "--transport", "-t", "--scope", "-s", "--env", "-e", "--header", "-H":
		default:
			// Do not echo arbitrary input: a mistaken flag may contain a secret.
			return "", fmt.Errorf("unknown install option; run /mcp install for help, or use -- before the executable to pass subprocess flags")
		}

		value := inline
		if !hasInline {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return "", fmt.Errorf("%s requires a value", option)
			}
			i++
			value = args[i]
		}
		if value == "" {
			return "", fmt.Errorf("%s requires a nonempty value", option)
		}
		switch option {
		case "--transport", "-t":
			switch value {
			case "http", "streamable-http":
				cfg.Transport = "streamable-http"
			case "stdio", "sse":
				cfg.Transport = value
			default:
				return "", fmt.Errorf("transport must be stdio, http, or sse")
			}
		case "--scope", "-s":
			if value != "local" && value != "project" && value != "user" {
				return "", fmt.Errorf("scope must be local, project, or user")
			}
			scope = value
		case "--env", "-e":
			key, val, ok := strings.Cut(value, "=")
			if !ok || !validEnvName(key) || strings.ContainsRune(val, '\x00') {
				return "", fmt.Errorf("%s requires KEY=value with a valid environment variable name", option)
			}
			if cfg.Env == nil {
				cfg.Env = map[string]string{}
			}
			cfg.Env[key] = val
		case "--header", "-H":
			key, val, ok := strings.Cut(value, ":")
			key = strings.TrimSpace(key)
			if !ok || !validHeaderName(key) || !validHeaderValue(val) {
				return "", fmt.Errorf("%s requires a valid HTTP header in 'Name: value' form", option)
			}
			if cfg.Headers == nil {
				cfg.Headers = map[string]string{}
			}
			cfg.Headers[http.CanonicalHeaderKey(key)] = strings.TrimSpace(val)
		}
	}

	if len(positional) < 2 {
		return "", fmt.Errorf("usage: /mcp install [options] <name> <commandOrUrl> [args...]")
	}
	name, target := positional[0], positional[1]
	if !validServerName(name) {
		return "", fmt.Errorf("server name must be nonempty and contain no whitespace or control characters")
	}
	if strings.TrimSpace(target) == "" || strings.ContainsRune(target, '\x00') {
		return "", fmt.Errorf("server command or URL must be nonempty and contain no NUL bytes")
	}
	if cfg.Transport == "stdio" {
		if len(cfg.Headers) != 0 {
			return "", fmt.Errorf("--header requires --transport http or sse")
		}
		if strings.Contains(target, "://") {
			return "", fmt.Errorf("a server URL requires --transport http or sse")
		}
		cfg.Command = target
		cfg.Args = positional[2:]
		for _, arg := range cfg.Args {
			if strings.ContainsRune(arg, '\x00') {
				return "", fmt.Errorf("subprocess arguments must not contain NUL bytes")
			}
		}
	} else {
		if len(cfg.Env) != 0 {
			return "", fmt.Errorf("--env requires --transport stdio; use --header for HTTP authentication")
		}
		if len(positional) != 2 {
			return "", fmt.Errorf("HTTP/SSE servers do not accept subprocess arguments")
		}
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return "", fmt.Errorf("server URL must be an absolute HTTP(S) URL without embedded credentials or a fragment")
		}
		cfg.URL = target
	}

	path, err := serverConfigPath(scope, cwd)
	if err != nil {
		return "", err
	}
	if err := installServerConfig(path, name, cfg); err != nil {
		return "", err
	}
	return fmt.Sprintf("Installed MCP server %q configuration to %s.\n\nRun /reload-ext to reload MCP tools.", name, path), nil
}

func validServerName(name string) bool {
	return name != "" && strings.IndexFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

// serverConfigPath keeps install and uninstall scope resolution identical.
func serverConfigPath(scope, cwd string) (string, error) {
	switch scope {
	case "user":
		return filepath.Join(zotHome(), "mcp.json"), nil
	case "local", "project":
		if cwd == "" {
			return "", fmt.Errorf("%s scope requires a working directory; use --scope user for global configuration", scope)
		}
		if scope == "project" {
			return filepath.Join(cwd, ".mcp.json"), nil
		}
		return filepath.Join(cwd, ".zot", "mcp.json"), nil
	default:
		return "", fmt.Errorf("scope must be local, project, or user")
	}
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for _, c := range value {
		if (c < 32 && c != '\t') || c == 127 {
			return false
		}
	}
	return true
}

func installServerConfig(path, name string, server ServerConfig) error {
	encoded, err := json.Marshal(server)
	if err != nil {
		return err
	}
	return editServerConfig(path, func(servers map[string]json.RawMessage) error {
		if _, exists := servers[name]; exists {
			return fmt.Errorf("server %q already exists in %s", name, path)
		}
		servers[name] = encoded
		return nil
	})
}

// The extension dispatches slash commands concurrently. Serialize configuration
// edits so install and uninstall cannot overwrite each other's changes.
var serverConfigMu sync.Mutex

// editServerConfig preserves fields owned by other MCP clients rather than
// round-tripping the shared file through our narrower Config type. An update
// error leaves the original file untouched, including when it does not exist.
func editServerConfig(path string, update func(map[string]json.RawMessage) error) error {
	serverConfigMu.Lock()
	defer serverConfigMu.Unlock()

	root := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(strings.TrimSpace(string(data))) != 0 {
		if err := json.Unmarshal(data, &root); err != nil || root == nil {
			return fmt.Errorf("config %s must contain a JSON object", path)
		}
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := root["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("mcpServers in %s must be a JSON object", path)
		}
		if servers == nil {
			servers = map[string]json.RawMessage{}
		}
	}
	if err := update(servers); err != nil {
		return err
	}
	root["mcpServers"], err = json.Marshal(servers)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

func readConfigFile(path string) (Config, error) {
	cfg := Config{MCPServers: map[string]ServerConfig{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]ServerConfig{}
	}
	return cfg, nil
}

func writeConfigFile(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// 0o600: mcp.json can contain auth headers / tokens.
	return writeFileAtomic(path, data, 0o600)
}
