// config.go — MCP server configuration loading.
//
// Reads standard MCP config files (same format as Claude Desktop, Cursor, etc.)
// and merges them in this order; later files replace same-named servers:
//
//  1. $XDG_CONFIG_HOME/mcp/mcp.json (default ~/.config/mcp/mcp.json)
//  2. ~/.agents/mcp.json
//  3. ~/.agents/mcp/mcp.json
//  4. $ZOT_HOME/mcp.json
//  5. .mcp.json      (in the current working directory)
//  6. .zot/mcp.json  (overrides the project config)
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ServerConfig describes one MCP server entry.
type ServerConfig struct {
	// Stdio transport fields
	Command string            `json:"command,omitempty"` // executable to spawn
	Args    []string          `json:"args,omitempty"`    // arguments
	Env     map[string]string `json:"env,omitempty"`     // extra env vars
	Cwd     string            `json:"cwd,omitempty"`     // working dir; ~ and relative paths resolve against the project

	// HTTP transport fields
	Type string `json:"type,omitempty"` // Claude Code alias: stdio | http | sse
	Transport string            `json:"transport,omitempty"` // "stdio" (default) | "streamable-http" | "sse"
	URL       string            `json:"url,omitempty"`       // server URL for HTTP transports
	Headers   map[string]string `json:"headers,omitempty"`   // custom HTTP headers

	// Timeouts (in seconds)
	ConnectTimeout int `json:"connectTimeout,omitempty"` // connection timeout (default: 30)
	RequestTimeout int `json:"requestTimeout,omitempty"` // per-request timeout (default: 60)
	IdleTimeout    int `json:"idleTimeout,omitempty"`    // idle timeout before stopping (default: 300)
	// Millisecond aliases (zot-mcp compatibility); take precedence, rounded up to whole seconds.
	ConnectTimeoutMs int `json:"connectTimeoutMs,omitempty"`
	RequestTimeoutMs int `json:"requestTimeoutMs,omitempty"`

	Disabled bool `json:"disabled,omitempty"` // keep configured, never start
}

// Config is the top-level MCP configuration.
type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// zotHome returns the zot state directory.
func zotHome() string {
	if h := os.Getenv("ZOT_HOME"); h != "" {
		return h
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "zot")
	}
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "zot")
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "zot")
	default: // linux, freebsd, etc.
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "state", "zot")
	}
}

// loadConfig reads and merges global + project MCP configs.
// cwd is the current working directory (for project config lookup).
func loadConfig(cwd string) (Config, error) {
	cfg := Config{MCPServers: make(map[string]ServerConfig)}

	for _, globalPath := range globalConfigPaths() {
		if err := mergeConfig(&cfg, globalPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return cfg, fmt.Errorf("global config %s: %w", globalPath, err)
		}
	}

	// Later files replace entire server entries, not individual fields.
	if cwd != "" {
		for _, relative := range []string{".mcp.json", filepath.Join(".zot", "mcp.json")} {
			projectPath := filepath.Join(cwd, relative)
			if err := mergeConfig(&cfg, projectPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return Config{}, fmt.Errorf("project config %s: %w", projectPath, err)
			}
		}
	}

	var expansionErrors []error
	for name, srv := range cfg.MCPServers {
		// ponytail: disabled servers vanish from /mcp status; keep them listed when status grows a "disabled" state.
		if srv.Disabled {
			delete(cfg.MCPServers, name)
			continue
		}
		if err := expandServerEnv(&srv, cwd); err != nil {
			delete(cfg.MCPServers, name)
			expansionErrors = append(expansionErrors, fmt.Errorf("server %q: %w", name, err))
			continue
		}
		cfg.MCPServers[name] = srv
	}
	return cfg, errors.Join(expansionErrors...)
}

// globalConfigPaths lists user-wide config files, lowest precedence first.
// ~/.config/mcp is a cross-client convention (zot-mcp and others hardcode it
// on every OS), not the platform config dir; the platform-native zot location
// is $ZOT_HOME/mcp.json.
func globalConfigPaths() []string {
	home, _ := os.UserHomeDir()
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return []string{
		filepath.Join(configHome, "mcp", "mcp.json"),
		filepath.Join(home, ".agents", "mcp.json"),
		filepath.Join(home, ".agents", "mcp", "mcp.json"),
		filepath.Join(zotHome(), "mcp.json"),
	}
}

// Expand Claude Code's braced syntax and zot-mcp's `$env:NAME`, not shell
// expressions or bare $VAR.
var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}|\$env:([A-Za-z_][A-Za-z0-9_]*)`)

func expandServerEnv(srv *ServerConfig, cwd string) error {
	var missing []error
	expand := func(field, value string) string {
		return envReference.ReplaceAllStringFunc(value, func(ref string) string {
			parts := envReference.FindStringSubmatch(ref)
			name := parts[1] + parts[4] // exactly one alternative matched
			if value, ok := os.LookupEnv(name); ok {
				return value
			}
			if parts[2] != "" {
				return parts[3]
			}
			// Never include field values: they may contain credentials.
			missing = append(missing, fmt.Errorf("%s: environment variable %s is not set", field, name))
			return ref
		})
	}
	srv.Command = expand("command", srv.Command)
	if srv.Cwd = expand("cwd", srv.Cwd); srv.Cwd != "" {
		srv.Cwd = resolvePath(srv.Cwd, cwd)
	}
	for i, arg := range srv.Args {
		srv.Args[i] = expand(fmt.Sprintf("args[%d]", i), arg)
	}
	for k, value := range srv.Env {
		srv.Env[k] = expand("env", value)
	}
	srv.URL = expand("url", srv.URL)
	for k, value := range srv.Headers {
		srv.Headers[k] = expand("headers", value)
	}
	return errors.Join(missing...)
}

// resolvePath expands a leading ~ and anchors relative paths at base.
func resolvePath(p, base string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) && base != "" {
		p = filepath.Join(base, p)
	}
	return p
}

// mergeConfig reads a JSON config file and merges its servers into cfg.
func mergeConfig(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var file Config
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for name, srv := range file.MCPServers {
		// Apply defaults
		if srv.Transport == "" {
			switch srv.Type {
			case "", "stdio": srv.Transport = "stdio"
			case "http": srv.Transport = "streamable-http"
			case "sse": srv.Transport = "sse"
			default: return fmt.Errorf("server %q: unsupported transport type", name)
			}
		}
		if srv.ConnectTimeoutMs > 0 {
			srv.ConnectTimeout, srv.ConnectTimeoutMs = (srv.ConnectTimeoutMs+999)/1000, 0
		}
		if srv.RequestTimeoutMs > 0 {
			srv.RequestTimeout, srv.RequestTimeoutMs = (srv.RequestTimeoutMs+999)/1000, 0
		}
		if srv.ConnectTimeout == 0 {
			srv.ConnectTimeout = 30
		}
		if srv.RequestTimeout == 0 {
			srv.RequestTimeout = 60
		}
		if srv.IdleTimeout == 0 {
			srv.IdleTimeout = 300
		}
		cfg.MCPServers[name] = srv
	}
	return nil
}
