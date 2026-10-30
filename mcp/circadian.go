package mcp

import (
	"context"
	"encoding/json"

	"github.com/kungfusheep/hue/circadian"
	protocol "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterCircadianTools exposes control of the engine owned by this process.
func RegisterCircadianTools(s *server.MCPServer, e *circadian.Engine) {
	for _, action := range []string{"status", "pause", "resume"} {
		tool := protocol.NewTool("circadian_"+action,
			protocol.WithDescription(action+" circadian lighting for Kitchen and Bedroom"))
		s.AddTool(tool, circadianHandler(e, action))
	}
}

func circadianHandler(e *circadian.Engine, action string) server.ToolHandlerFunc {
	return func(_ context.Context, _ protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		switch action {
		case "pause":
			e.Pause()
		case "resume":
			e.Resume()
		}
		result, err := json.Marshal(e.Status())
		if err != nil {
			return protocol.NewToolResultError(err.Error()), nil
		}
		return protocol.NewToolResultText(string(result)), nil
	}
}
