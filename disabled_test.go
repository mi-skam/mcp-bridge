package main

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisabledServerNeverConnects(t *testing.T) {
	s := newManagedServer("off", ServerConfig{Disabled: true, Command: "must-not-run"}, "", log.New(io.Discard, "", 0))
	if s.status() != "off: DISABLED" || !strings.Contains(s.detailStatus(0), "status: disabled") {
		t.Fatal("disabled status missing")
	}
	if s.start(context.Background()) == nil {
		t.Fatal("disabled server started")
	}
	if s.login(context.Background(), func(string) { t.Fatal("browser opened") }) == nil {
		t.Fatal("disabled login accepted")
	}
	b := &bridge{servers: map[string]*managedServer{"off": s}, logger: s.logger}
	if err := b.startAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.refreshToolCache(context.Background(), filepath.Join(t.TempDir(), "cache.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogNotifications(t *testing.T) {
	s := newManagedServer("fx", ServerConfig{}, "", log.New(io.Discard, "", 0))
	var messages []string
	s.events = func(level, msg string) { messages = append(messages, level+":"+msg) }
	opts := s.clientOptions()
	opts.ResourceListChangedHandler(context.Background(), nil)
	opts.PromptListChangedHandler(context.Background(), nil)
	if len(messages) != 2 || !strings.Contains(messages[0], "resources list changed") || !strings.Contains(messages[1], "prompts list changed") {
		t.Fatal(messages)
	}
}
