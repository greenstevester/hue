package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/kungfusheep/hue/circadian"
	"github.com/kungfusheep/hue/client"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type unusedBridge struct{}

func (unusedBridge) UpdateLight(context.Context, string, client.LightUpdate) error { return nil }

func TestCircadianToolsPauseResumeAndReportStatus(t *testing.T) {
	c := circadian.DefaultConfig()
	c.Enabled = true
	e, err := circadian.NewEngine(c, unusedBridge{}, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s := server.NewMCPServer("test", "1")
	RegisterCircadianTools(s, e)
	for _, action := range []string{"status", "resume", "pause"} {
		result, err := circadianHandler(e, action)(context.Background(), protocol.CallToolRequest{})
		if err != nil || result.IsError {
			t.Fatalf("%s: %+v %v", action, result, err)
		}
		if action == "resume" && e.Status().Paused {
			t.Fatal("resume did not take effect")
		}
		if action == "pause" && !e.Status().Paused {
			t.Fatal("pause did not take effect")
		}
	}
}
