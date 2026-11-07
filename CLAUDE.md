# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Philips Hue MCP (Model Context Protocol) server and CLI written in Go. It provides comprehensive control of Philips Hue v2 API, enabling both AI agents and command-line users to control lighting with native effects, advanced sequencing, and real-time event streaming.

## Development Commands

### Building
```bash
# Build the binary
go build -o hue

# Build with specific OS/architecture
GOOS=linux GOARCH=amd64 go build -o hue
```

### Testing
```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Test specific package
go test ./client/...
go test ./mcp/...
```

### Running
```bash
# Run as MCP server (for Claude Desktop integration)
./hue

# Run CLI commands
./hue lights list
./hue discover
./hue stream

# Required environment variables
export HUE_BRIDGE_IP="192.168.x.x"
export HUE_USERNAME="your-api-username"
```

## Architecture

### Core Components

1. **Client Package (`client/`)** - Low-level Hue API v2 client
   - `hue.go` - Main client with HTTP methods and connection management
   - `types.go` - All API data structures (Light, Group, Scene, etc.)
   - `device.go`, `room.go`, `sensor.go` - Domain-specific API methods
   - `crud.go` - Create/Update/Delete operations
   - `events.go` - Real-time SSE event streaming
   - `entertainment.go` - Entertainment area and DTLS streaming support

2. **MCP Package (`mcp/`)** - Model Context Protocol server implementation
   - `mcp.go` - Tool handlers for Claude Desktop integration
   - `scheduler.go` - Complex effect scheduling and sequencing
   - `scene_cache.go` - In-memory scene caching for instant recall
   - `events.go` - Event stream management for MCP clients
   - Each file maps to specific tool categories exposed to Claude

3. **CMD Package (`cmd/`)** - CLI command implementations
   - `root.go` - Main CLI setup and global flags
   - `lights.go`, `groups.go`, `scenes.go` - Resource-specific commands
   - `effects.go` - Pre-built effect commands (flash, pulse, strobe)
   - `stream.go` - Real-time event streaming CLI
   - `discover.go` - Bridge discovery functionality
   - `resolver.go` - Name-to-ID resolution for user-friendly commands

4. **Scheduler Package (`scheduler/`)** - Asynchronous command execution
   - `scheduler.go` - Core scheduling engine for non-blocking operations
   - `effects.go` - Pre-built effect implementations

5. **Effects Package (`effects/`)** - Native Hue effect definitions
   - Defines all supported native effects (candle, fire, sparkle, etc.)

6. **Circadian Package (`circadian/`)** - Adaptive lighting for Kitchen and Bedroom only; see CIRCADIAN.md
   - `engine.go` - Follows Bridge events, holds a light after a manual change, sends at most one update per second
   - `schedule.go` - Daily phases from wake time, sunrise, sunset and bedtime
   - `config.go` - Loads and validates the JSON configuration
   - `session.go` - Discovers both rooms, takes the one-controller-per-Bridge lock, starts paused

7. **Web Package (`webui/`)** - Dashboard served by `hue circadian serve`
   - `server.go` - Status and settings requests; saves changes back to the configuration file
   - `assets/` - Page, styles and script, embedded in the binary

### Key Design Patterns

- **Non-blocking by default**: All MCP operations run asynchronously unless explicitly set to synchronous
- **Name resolution**: CLI automatically resolves friendly names to UUIDs
- **Dual mode**: Single binary functions as both MCP server and CLI tool
- **Shared client**: Both MCP and CLI use the same Hue client implementation
- **Effect layering**: Multiple effects can run simultaneously on different lights

### Important Implementation Details

- The main.go intelligently routes between MCP server mode and CLI mode based on arguments
- HTTP client uses InsecureSkipVerify for self-signed Hue bridge certificates
- SSE event streaming maintains persistent connection with automatic reconnection
- Scene caching stores complex command sequences in memory for instant replay
- Scheduler manages concurrent effect execution with proper cleanup on shutdown

## Key Files to Understand

- `main.go` - Entry point, mode selection (MCP vs CLI)
- `client/hue.go` - Core HTTP methods and API communication
- `mcp/mcp.go` - All MCP tool handler implementations
- `cmd/root.go` - CLI command structure and initialization
- `scheduler/scheduler.go` - Async execution engine