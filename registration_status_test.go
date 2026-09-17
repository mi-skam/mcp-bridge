package main

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestRegistrationStatus(t *testing.T) {
	b := &bridge{mapping: map[string]toolMapping{
		"one":   {serverName: "test"},
		"other": {serverName: "another"},
	}}
	s := &managedServer{name: "test", state: stateReady, tools: []mcp.Tool{{Name: "one"}, {Name: "two"}}}
	if got := b.registeredToolCount("test"); got != 1 {
		t.Fatalf("registered = %d, want 1", got)
	}
	if got := b.registeredToolCount("missing"); got != 0 {
		t.Fatalf("missing server registered = %d", got)
	}
	detail := s.detailStatus(b.registeredToolCount("test"))
	for _, want := range []string{"tools discovered (last successful connection): 2", "session (deferred): 1", "/reload-ext", "RECENT LIFECYCLE LOG"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("missing %q in %s", want, detail)
		}
	}
	if strings.Contains(s.detailStatus(2), "/reload-ext") {
		t.Fatal("equal counts must not trigger a mismatch warning")
	}
	s.state = stateStopped
	detail = s.detailStatus(2)
	if strings.Contains(detail, "tools discovered") || !strings.Contains(detail, "not live): 2") {
		t.Fatalf("sleeping tools must not be presented as live discovery: %s", detail)
	}
}

func TestLoginSlotIsExclusive(t *testing.T) {
	s := &managedServer{name: "n8n"}
	if !s.beginLogin() || s.beginLogin() {
		t.Fatal("second concurrent auth must be refused")
	}
	s.endLogin()
	if !s.beginLogin() {
		t.Fatal("slot must free after endLogin")
	}
	if !strings.Contains(s.detailStatus(0), "AUTH started") {
		t.Fatal("auth start must appear in the lifecycle log")
	}
}

func TestLogoutStatusIsNotSleeping(t *testing.T) {
	s := &managedServer{name: "n8n", state: stateStopped, tools: []mcp.Tool{{Name: "a"}}}
	if !strings.Contains(s.status(), "SLEEPING") {
		t.Fatalf("precondition: %s", s.status())
	}
	s.markLoggedOut()
	if got := s.status(); !strings.Contains(got, "NOT AUTHORIZED — /mcp auth n8n") {
		t.Fatalf("after logout: %s", got)
	}
	if !strings.Contains(s.detailStatus(0), "LOGOUT") {
		t.Fatal("logout must appear in the lifecycle log")
	}
}
