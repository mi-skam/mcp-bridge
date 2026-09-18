package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func uninstallHelp() string {
	return `mcp-bridge uninstall

Usage:
  /mcp uninstall                        Show this help
  /mcp uninstall [options] <name>

Remove one MCP server configuration from the selected scope only.

Options:
  -s, --scope <local|project|user> Configuration scope (default: local)
      --global                   Alias for --scope user
      --project                  Alias for --scope project
      --                         Treat the remaining token as the server name

Scopes:
  local    <cwd>/.zot/mcp.json
  project  <cwd>/.mcp.json
  user     $ZOT_HOME/mcp.json

Examples:
  /mcp uninstall test-grep
  /mcp uninstall --scope project test-docs
  /mcp uninstall --scope user test-browser

Only the named entry is removed; other entries and configuration fields remain.
A missing entry is an error, not a request to search or delete across scopes.
Run /reload-ext to apply the change; running servers and tools are unchanged
until reload. A same-name entry in another scope may become active after reload.
OAuth credentials, cached tool definitions, executables, and packages are kept.
To remove stored OAuth credentials too, use /mcp logout <name> before uninstall.`
}

func handleUninstall(args []string, cwd string) (string, error) {
	if len(args) == 0 || (len(args) == 1 && args[0] == "help") {
		return uninstallHelp(), nil
	}

	scope := "local"
	var names []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			names = append(names, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			names = append(names, arg)
			continue
		}
		option, value, hasValue := strings.Cut(arg, "=")
		switch option {
		case "--help", "-h":
			if hasValue {
				return "", fmt.Errorf("%s does not accept a value", option)
			}
			return uninstallHelp(), nil
		case "--global", "--project":
			if hasValue {
				return "", fmt.Errorf("%s does not accept a value", option)
			}
			if option == "--global" {
				scope = "user"
			} else {
				scope = "project"
			}
		case "--scope", "-s":
			if !hasValue {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return "", fmt.Errorf("%s requires a value", option)
				}
				i++
				value = args[i]
			}
			if value != "local" && value != "project" && value != "user" {
				return "", fmt.Errorf("scope must be local, project, or user")
			}
			scope = value
		default:
			return "", fmt.Errorf("unknown uninstall option; run /mcp uninstall for help")
		}
	}
	if len(names) != 1 {
		return "", fmt.Errorf("usage: /mcp uninstall [options] <name> (exactly one server)")
	}
	name := names[0]
	if !validServerName(name) {
		return "", fmt.Errorf("server name must be nonempty and contain no whitespace or control characters")
	}
	path, err := serverConfigPath(scope, cwd)
	if err != nil {
		return "", err
	}
	if err := uninstallServerConfig(path, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed MCP server %q configuration from %s.\n\nRun /reload-ext to apply the change; running servers and tools are unchanged until reload. A same-name entry in another scope may become active after reload. OAuth credentials and cached tool definitions were kept.", name, path), nil
}

func uninstallServerConfig(path, name string) error {
	return editServerConfig(path, func(servers map[string]json.RawMessage) error {
		if _, exists := servers[name]; !exists {
			return fmt.Errorf("server %q is not configured in %s; select its scope with --scope local, project, or user", name, path)
		}
		delete(servers, name)
		return nil
	})
}
