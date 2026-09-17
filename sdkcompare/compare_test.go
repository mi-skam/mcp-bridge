package sdkcompare

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"
)

var fixtureBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sdkcompare")
	if err != nil {
		panic(err)
	}
	fixtureBin = filepath.Join(dir, "fixture")
	build := exec.Command("go", "build", "-o", fixtureBin, "./fixture")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("build fixture: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func both(t Target) []Client { return []Client{NewMCPGo(t), NewGoSDK(t)} }

func fixture() Target { return Target{Command: fixtureBin} }

func connected(t *testing.T, c Client, target Target) Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("%s connect: %v", c.Name(), err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// A1: tool catalogue identical after normalisation.
func TestA1_ListToolsEqual(t *testing.T) {
	var lists [][]Tool
	for _, c := range both(fixture()) {
		connected(t, c, fixture())
		tools, err := c.ListTools(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", c.Name(), err)
		}
		lists = append(lists, tools)
	}
	if len(lists[0]) != 5 {
		t.Fatalf("fixture exposes 5 tools, got %d", len(lists[0]))
	}
	if !reflect.DeepEqual(lists[0], lists[1]) {
		a, _ := json.MarshalIndent(lists[0], "", " ")
		b, _ := json.MarshalIndent(lists[1], "", " ")
		t.Fatalf("catalogue differs\nmcp-go:\n%s\ngo-sdk:\n%s", a, b)
	}
}

// A6: text, image, structured results identical.
func TestA6_ResultsEqual(t *testing.T) {
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"echo", map[string]any{"text": "héllo ✓"}},
		{"image", nil},
		{"structured", map[string]any{"n": 3}},
	}
	var results [][]Result
	for _, c := range both(fixture()) {
		connected(t, c, fixture())
		var rs []Result
		for _, call := range calls {
			r, err := c.Call(context.Background(), call.tool, call.args)
			if err != nil {
				t.Fatalf("%s %s: %v", c.Name(), call.tool, err)
			}
			rs = append(rs, r)
		}
		results = append(results, rs)
	}
	for i, call := range calls {
		if !reflect.DeepEqual(results[0][i], results[1][i]) {
			a, _ := json.Marshal(results[0][i])
			b, _ := json.Marshal(results[1][i])
			t.Errorf("%s differs\nmcp-go: %s\ngo-sdk: %s", call.tool, a, b)
		}
	}
}

// A7: request timeout honoured within +1s.
func TestA7_Timeout(t *testing.T) {
	const budget = 500 * time.Millisecond
	for _, c := range both(fixture()) {
		connected(t, c, fixture())
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		d, err := Timed(func() error {
			_, err := c.Call(ctx, "sleep", map[string]any{"ms": 5000})
			return err
		})
		cancel()
		if err == nil {
			t.Errorf("%s: expected timeout error", c.Name())
		}
		if d > budget+time.Second {
			t.Errorf("%s: returned after %v, budget %v", c.Name(), d, budget)
		}
		t.Logf("%s timeout returned in %v: %v", c.Name(), d, err)
	}
}

// A8: server dies mid-request → error, no goroutine leak.
func TestA8_ServerDies(t *testing.T) {
	for _, c := range both(fixture()) {
		before := runtime.NumGoroutine()
		connected(t, c, fixture())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := c.Call(ctx, "die", nil)
		cancel()
		if err == nil {
			t.Errorf("%s: expected transport error", c.Name())
		}
		c.Close()
		time.Sleep(200 * time.Millisecond)
		after := runtime.NumGoroutine()
		if after > before+2 {
			t.Errorf("%s: goroutines %d → %d after server death", c.Name(), before, after)
		}
		t.Logf("%s: err=%v goroutines %d→%d", c.Name(), err, before, after)
	}
}

// P1: connect latency stdio.
func TestP1_ConnectLatency(t *testing.T) {
	const n = 20
	medians := map[string]time.Duration{}
	for _, mk := range []func(Target) Client{NewMCPGo, NewGoSDK} {
		var ds []time.Duration
		for i := 0; i < n; i++ {
			c := mk(fixture())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			d, err := Timed(func() error { return c.Connect(ctx) })
			cancel()
			c.Close()
			if err != nil {
				t.Fatalf("%s: %v", c.Name(), err)
			}
			ds = append(ds, d)
			if i == n-1 {
				sort.Slice(ds, func(a, b int) bool { return ds[a] < ds[b] })
				medians[c.Name()] = ds[n/2]
			}
		}
	}
	report(t, "connect stdio median", medians)
}

// P2: tools/list latency stdio.
func TestP2_ListToolsLatency(t *testing.T) {
	const n = 20
	medians := map[string]time.Duration{}
	for _, c := range both(fixture()) {
		connected(t, c, fixture())
		var ds []time.Duration
		for i := 0; i < n; i++ {
			d, err := Timed(func() error { _, err := c.ListTools(context.Background()); return err })
			if err != nil {
				t.Fatalf("%s: %v", c.Name(), err)
			}
			ds = append(ds, d)
		}
		sort.Slice(ds, func(a, b int) bool { return ds[a] < ds[b] })
		medians[c.Name()] = ds[n/2]
	}
	report(t, "tools/list median", medians)
}

func report(t *testing.T, label string, m map[string]time.Duration) {
	a, b := m["mcp-go"], m["go-sdk"]
	ratio := float64(b) / float64(a)
	t.Logf("%s: mcp-go=%v go-sdk=%v ratio=%.2f", label, a, b, ratio)
	if ratio > 1.2 || ratio < 1/1.2 {
		t.Logf("NOTE: >20%% apart")
	}
}

// ------------------------------------------------------------ live (OAuth)

// liveTarget reads SDKCOMPARE_URL and SDKCOMPARE_STATE (credential file).
// Skips when unset. SDKCOMPARE_INTERACTIVE=1 permits the browser flow.
func liveTarget(t *testing.T) (Target, *OAuthOptions) {
	u := os.Getenv("SDKCOMPARE_URL")
	state := os.Getenv("SDKCOMPARE_STATE")
	if u == "" || state == "" {
		t.Skip("set SDKCOMPARE_URL and SDKCOMPARE_STATE for live OAuth scenarios")
	}
	o := &OAuthOptions{StatePath: state, ResourceURL: u, Interactive: os.Getenv("SDKCOMPARE_INTERACTIVE") == "1", Open: openBrowser}
	return Target{URL: u, OAuth: o}, o
}

func openBrowser(u string) {
	exec.Command("open", u).Start() // ponytail: darwin only; harness runs on the dev machine
}

// A2/A3: stored credentials (valid or refreshable) → both connect without a browser.
func TestA2_A3_StoredCredentials(t *testing.T) {
	target, o := liveTarget(t)
	o.Interactive = false
	for _, c := range both(target) {
		connected(t, c, target)
		tools, err := c.ListTools(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", c.Name(), err)
		}
		t.Logf("%s: %d tools", c.Name(), len(tools))
	}
}

// A4: credentials unusable → both fail with auth-required, no browser.
// Run with SDKCOMPARE_STATE pointing at a file whose client was purged.
func TestA4_DeadCredentials(t *testing.T) {
	if os.Getenv("SDKCOMPARE_DEAD") != "1" {
		t.Skip("set SDKCOMPARE_DEAD=1 with a state file whose registration is gone")
	}
	target, o := liveTarget(t)
	o.Interactive = false
	for _, c := range both(target) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := c.Connect(ctx)
		cancel()
		c.Close()
		if err == nil {
			t.Errorf("%s: connected with dead credentials", c.Name())
			continue
		}
		t.Logf("%s: %v (auth-required=%v)", c.Name(), err, errors.Is(err, ErrAuthRequired))
	}
}

// A5: fresh interactive auth with one SDK, then the other SDK loads the file.
func TestA5_InteractiveCrossLoad(t *testing.T) {
	target, o := liveTarget(t)
	if !o.Interactive {
		t.Skip("set SDKCOMPARE_INTERACTIVE=1")
	}
	os.Remove(o.StatePath)
	first := os.Getenv("SDKCOMPARE_FIRST")
	if first == "" {
		first = "go-sdk"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	var a, b Client
	if first == "go-sdk" {
		a, b = NewGoSDK(target), NewMCPGo(target)
		if err := a.Connect(ctx); err != nil {
			t.Fatalf("go-sdk interactive: %v", err)
		}
	} else {
		if err := o.LoginMCPGo(ctx); err != nil {
			t.Fatalf("mcp-go login: %v", err)
		}
		a, b = NewMCPGo(target), NewGoSDK(target)
		if err := a.Connect(ctx); err != nil {
			t.Fatalf("mcp-go after login: %v", err)
		}
	}
	defer a.Close()
	o.Interactive = false
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("%s loading %s credentials: %v", b.Name(), a.Name(), err)
	}
	defer b.Close()
	ta, _ := a.ListTools(ctx)
	tb, _ := b.ListTools(ctx)
	if !reflect.DeepEqual(ta, tb) {
		t.Fatal("catalogues differ across SDKs on the same credentials")
	}
	t.Logf("%s authorized, %s reused the file, %d tools", a.Name(), b.Name(), len(ta))
}
