package main

import (
 "path/filepath"
 "testing"
)

func TestThreeLevelPrecedence(t *testing.T) {
 home, project := t.TempDir(), t.TempDir()
 t.Setenv("ZOT_HOME", home)
 writeJSON(t, filepath.Join(home,"mcp.json"), `{"mcpServers":{"shared":{"transport":"streamable-http","url":"https://old.test","headers":{"Authorization":"synthetic"}},"global":{"command":"global"}}}`)
 writeJSON(t, filepath.Join(project,".mcp.json"), `{"mcpServers":{"shared":{"command":"adapter"},"http":{"type":"http","url":"https://example.test/mcp"},"sse":{"type":"sse","url":"https://example.test/sse"}}}`)
 cfg, err := loadConfig(project)
 if err != nil { t.Fatal(err) }
 s:=cfg.MCPServers["shared"]
 if s.Command!="adapter" || s.Transport!="stdio" || s.URL!="" || len(s.Headers)!=0 { t.Fatal("project must replace the whole global server entry") }
 if cfg.MCPServers["http"].Transport!="streamable-http" || cfg.MCPServers["sse"].Transport!="sse" { t.Fatal("Claude transport aliases not resolved") }
 writeJSON(t, filepath.Join(project,".zot","mcp.json"), `{"mcpServers":{"shared":{"command":"zot-specific"}}}`)
 cfg, err=loadConfig(project)
 if err!=nil { t.Fatal(err) }
 if cfg.MCPServers["shared"].Command!="zot-specific" || len(cfg.MCPServers)!=4 { t.Fatal("zot override or unrelated server preservation failed") }
 writeJSON(t,filepath.Join(project,".mcp.json"), `{invalid`)
 cfg,err=loadConfig(project)
 if err==nil || len(cfg.MCPServers)!=0 { t.Fatal("invalid project must not silently use globals") }
}
