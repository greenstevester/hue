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
