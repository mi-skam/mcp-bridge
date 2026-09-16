// config.go — MCP server configuration loading.
//
// Reads standard MCP config files (same format as Claude Desktop, Cursor, etc.)
// from three locations:
//
//  1. Global:  $ZOT_HOME/mcp.json
//  2. Project: .mcp.json          (in the current working directory)
//  3. Zot-specific: .zot/mcp.json (overrides the project config)
//
// Project config overrides global config per-server (shallow merge).
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
)

// ServerConfig describes one MCP server entry.
type ServerConfig struct {
	// Stdio transport fields
	Command string            `json:"command,omitempty"` // executable to spawn
	Args    []string          `json:"args,omitempty"`    // arguments
	Env     map[string]string `json:"env,omitempty"`     // extra env vars

	// HTTP transport fields
	Type string `json:"type,omitempty"` // Claude Code alias: stdio | http | sse
	Transport string            `json:"transport,omitempty"` // "stdio" (default) | "streamable-http" | "sse"
	URL       string            `json:"url,omitempty"`       // server URL for HTTP transports
	Headers   map[string]string `json:"headers,omitempty"`   // custom HTTP headers

	// Timeouts (in seconds)
	ConnectTimeout int `json:"connectTimeout,omitempty"` // connection timeout (default: 30)
	RequestTimeout int `json:"requestTimeout,omitempty"` // per-request timeout (default: 60)
	IdleTimeout    int `json:"idleTimeout,omitempty"`    // idle timeout before stopping (default: 300)
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

	// 1. Global config
	globalPath := filepath.Join(zotHome(), "mcp.json")
	if err := mergeConfig(&cfg, globalPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, fmt.Errorf("global config %s: %w", globalPath, err)
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
		if err := expandServerEnv(&srv); err != nil {
			delete(cfg.MCPServers, name)
			expansionErrors = append(expansionErrors, fmt.Errorf("server %q: %w", name, err))
			continue
		}
		cfg.MCPServers[name] = srv
	}
	return cfg, errors.Join(expansionErrors...)
}

// Expand only Claude Code's braced syntax, not shell expressions or bare $VAR.
var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

func expandServerEnv(srv *ServerConfig) error {
	var missing []error
	expand := func(field, value string) string {
		return envReference.ReplaceAllStringFunc(value, func(ref string) string {
			parts := envReference.FindStringSubmatch(ref)
			if value, ok := os.LookupEnv(parts[1]); ok {
				return value
			}
			if parts[2] != "" {
				return parts[3]
			}
			// Never include field values: they may contain credentials.
			missing = append(missing, fmt.Errorf("%s: environment variable %s is not set", field, parts[1]))
			return ref
		})
	}
	srv.Command = expand("command", srv.Command)
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
