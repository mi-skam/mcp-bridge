package main

import (
 "strings"
 "testing"
)

func TestMCPCommandsAndStatusSeparate(t *testing.T) {
 if strings.Contains(mcpHelp(nil), "no servers configured") { t.Fatal("help includes status") }
 if !strings.Contains(mcpHelp(nil), "/mcp status <server>") { t.Fatal("missing detailed status command") }
 if strings.Contains(mcpOverview(nil), "/mcp") { t.Fatal("status includes command menu") }
 s := &managedServer{name:"test"}
 s.mu.Lock()
 for i:=0; i<12; i++ { s.recordEvent("CONNECTING") }
 s.mu.Unlock()
 if len(s.recent)!=8 { t.Fatal("unbounded log") }
 detail:=s.detailStatus(0)
 if !strings.Contains(detail,"RECENT LIFECYCLE LOG") || strings.Count(detail,"CONNECTING")!=8 { t.Fatal("missing log excerpt") }
}
