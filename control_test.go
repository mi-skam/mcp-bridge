package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestControl(t *testing.T) {
	b := inMemoryBridge(t)
	r := b.controlTool(json.RawMessage(`{"server":"fx","action":"ping"}`))
	if r.IsError || !strings.Contains(text(r), `"ok":true`) {
		t.Fatal(r)
	}
	for _, input := range []string{
		`{"server":"fx","action":"logging/set","level":"bogus"}`,
		`{"server":"fx","action":"complete"}`,
		`{"server":"fx","action":"complete","ref":{"type":"ref/prompt"},"argument":{"name":"who","value":"a"}}`,
		`{"server":"fx","action":"ping","unknown":true}`,
		`null`,
	} {
		if !b.controlTool(json.RawMessage(input)).IsError {
			t.Errorf("accepted %s", input)
		}
	}
}
