package main

import (
	"strings"
	"testing"
)

func TestMCPHelpAdvertisesInstall(t *testing.T) {
	help := mcpHelp(nil)
	for _, want := range []string{"/mcp install", "<name>", "<commandOrUrl>"} {
		if !strings.Contains(help, want) {
			t.Errorf("help must advertise generic install syntax %q:\n%s", want, help)
		}
	}
	for _, obsolete := range []string{"/mcp setup", "/mcp install add", "/mcp install templates", "/mcp install list", "<template>", "--name", "/mcp install <server>"} {
		if strings.Contains(help, obsolete) {
			t.Errorf("help advertises obsolete syntax %q", obsolete)
		}
	}
	for _, option := range []string{"--transport", "--scope", "--env", "--header"} {
		if !strings.Contains(help, option) {
			t.Errorf("help missing install option %q", option)
		}
	}
}

func TestMCPHelpAdvertisesUninstall(t *testing.T) {
	help := mcpHelp(nil)
	for _, line := range strings.Split(help, "\n") {
		if strings.Contains(line, "/mcp uninstall") && strings.Contains(line, "<name>") {
			return
		}
	}
	t.Fatalf("help must advertise /mcp uninstall with a server name:\n%s", help)
}

func TestMCPHelpAdvertisesListAndRequiredStatusServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    *bridge
	}{
		{"nil bridge", nil},
		{"empty bridge", &bridge{servers: map[string]*managedServer{}}},
		{"configured bridge", &bridge{servers: map[string]*managedServer{"test": {name: "test"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			help := mcpHelp(tc.b)
			var list, status bool
			for _, line := range strings.Split(help, "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 || fields[0] != "/mcp" {
					continue
				}
				switch fields[1] {
				case "list":
					list = true
				case "status":
					status = true
					if len(fields) < 3 || fields[2] != "<server>" {
						t.Errorf("status must require <server>, not advertise aggregate status: %q", line)
					}
				}
			}
			if !list || !status {
				t.Errorf("menu must advertise /mcp list and /mcp status <server>:\n%s", help)
			}
		})
	}
}

func TestMCPCommandsAndStatusSeparate(t *testing.T) {
	if strings.Contains(mcpHelp(nil), "no servers configured") {
		t.Fatal("help includes status")
	}
	if !strings.Contains(mcpHelp(nil), "/mcp status <server>") {
		t.Fatal("missing detailed status command")
	}
	if strings.Contains(mcpOverview(nil), "/mcp") {
		t.Fatal("status includes command menu")
	}
	s := &managedServer{name: "test"}
	s.mu.Lock()
	for i := 0; i < 12; i++ {
		s.recordEvent("CONNECTING")
	}
	s.mu.Unlock()
	if len(s.recent) != 8 {
		t.Fatal("unbounded log")
	}
	detail := s.detailStatus(0)
	if !strings.Contains(detail, "RECENT LIFECYCLE LOG") || strings.Count(detail, "CONNECTING") != 8 {
		t.Fatal("missing log excerpt")
	}
}
