package main

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSubscriptionsAndProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ServerOptions{
		SubscribeHandler:   func(context.Context, *mcp.SubscribeRequest) error { return nil },
		UnsubscribeHandler: func(context.Context, *mcp.UnsubscribeRequest) error { return nil },
	})
	srv.AddResource(&mcp.Resource{Name: "note", URI: "mem://note"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	ms := newManagedServer("test", ServerConfig{RequestTimeout: 2}, "", log.New(io.Discard, "", 0))
	events := make(chan string, 2)
	ms.events = func(_, msg string) { events <- msg }
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, ms.clientOptions()).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ms.client, ms.state = cs, stateReady
	if err := ms.resourceSubscription(ctx, "mem://note", true); err != nil {
		t.Fatal(err)
	}
	if ms.isIdle(-time.Second) {
		t.Fatal("subscribed server considered idle")
	}
	if err := ms.resourceSubscription(ctx, "mem://note", false); err != nil {
		t.Fatal(err)
	}
	if !ms.isIdle(-time.Second) {
		t.Fatal("unsubscribed server cannot sleep")
	}
	ms.clientOptions().ProgressNotificationHandler(ctx, &mcp.ProgressNotificationClientRequest{Params: &mcp.ProgressNotificationParams{ProgressToken: "echo:1", Progress: 1, Total: 2, Message: "working"}})
	select {
	case msg := <-events:
		if !strings.Contains(msg, "echo:1") || !strings.Contains(msg, "working") {
			t.Fatal(msg)
		}
	case <-ctx.Done():
		t.Fatal("missing progress")
	}
}
