package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kungfusheep/hue/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestAsyncBatchKeepsRunningAfterTheToolCallEnds(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			writes.Add(1)
		}
		fmt.Fprint(w, `{"errors":[],"data":[]}`)
	}))
	defer server.Close()
	c := client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{
		"commands": `[{"action":"light_on","target_id":"a"},{"action":"light_on","target_id":"b"},` +
			`{"action":"light_on","target_id":"c"}]`,
		"delay_ms": 50.0,
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := HandleBatchCommands(c)(ctx, request); err != nil {
		t.Fatal(err)
	}
	cancel() // newer mcp-go versions cancel each call's context when the call returns
	deadline := time.Now().Add(2 * time.Second)
	for writes.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := writes.Load(); got != 3 {
		t.Fatalf("batch stopped when the tool call ended: %d of 3 commands sent", got)
	}
}

func TestBatchClaimsOnlyTheLightsItTouches(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"errors":[],"data":[]}`)
	}))
	defer server.Close()
	c := client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	var claimed [][]string
	c.SetWriteGuard(func(_ context.Context, _ string, data interface{}) error {
		if a, ok := data.(client.Activity); ok && a.Active {
			claimed = append(claimed, a.Targets)
		}
		return nil
	})
	office := []map[string]interface{}{
		{"action": "light_on", "target_id": "office-lamp"},
		{"action": "light_color", "target_id": "office-strip", "value": "#FF0000"},
	}
	withGroup := append(office, map[string]interface{}{"action": "group_off", "target_id": "office"})
	ExecuteBatch(context.Background(), c, office, 0)
	ExecuteBatch(context.Background(), c, withGroup, 0)
	if len(claimed) != 2 {
		t.Fatalf("expected two claims, got %v", claimed)
	}
	if got := strings.Join(claimed[0], ","); got != "office-lamp,office-strip" {
		t.Fatalf("light-only batch claimed %q, want only its own lights", got)
	}
	if claimed[1] != nil {
		t.Fatalf("batch with a group command claimed %v, want every managed light", claimed[1])
	}
}
