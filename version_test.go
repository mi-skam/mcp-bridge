package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestExtensionJSONVersionMatches(t *testing.T) {
	data, err := os.ReadFile("extension.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Version string }
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != version {
		t.Fatalf("extension.json version %q != version.go %q", manifest.Version, version)
	}
}
