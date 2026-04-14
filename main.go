package main

import (
	"log"
)

// Main entry point for Oido Gmail MCP Server.
// This runs as a standalone process using stdio transport for Qwen CLI.
func main() {
	log.Println("Starting Oido Gmail MCP Server v1.0.0...")
	RunMCPServer()
}
