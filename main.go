package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
)

// configJSON holds settings parsed from --config flag.
// Field names match env var names for simple mapping.
type configJSON struct {
	GMAIL_EMAIL         string `json:"GMAIL_EMAIL"`
	GMAIL_PASSWORD      string `json:"GMAIL_PASSWORD"`
	GMAIL_IMAP_HOST     string `json:"GMAIL_IMAP_HOST"`
	GMAIL_IMAP_PORT     string `json:"GMAIL_IMAP_PORT"`
	GMAIL_SMTP_HOST     string `json:"GMAIL_SMTP_HOST"`
	GMAIL_SMTP_PORT     string `json:"GMAIL_SMTP_PORT"`
	GMAIL_ALLOW_SEND    string `json:"GMAIL_ALLOW_SEND"`
	GMAIL_ALLOW_RECEIVE string `json:"GMAIL_ALLOW_RECEIVE"`
}

// Main entry point for Oido Gmail MCP Server.
// This runs as a standalone process using stdio transport for Qwen CLI.
func main() {
	configStr := flag.String("config", "", "JSON string with Gmail settings (overrides .env, overridden by env vars)")
	flag.Parse()

	if *configStr != "" {
		var cfg configJSON
		if err := json.Unmarshal([]byte(*configStr), &cfg); err != nil {
			log.Fatalf("Failed to parse --config JSON: %v", err)
		}
		setIfNotEnv := func(key, val string) {
			if val != "" && os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
		setIfNotEnv("GMAIL_EMAIL", cfg.GMAIL_EMAIL)
		setIfNotEnv("GMAIL_PASSWORD", cfg.GMAIL_PASSWORD)
		setIfNotEnv("GMAIL_IMAP_HOST", cfg.GMAIL_IMAP_HOST)
		setIfNotEnv("GMAIL_IMAP_PORT", cfg.GMAIL_IMAP_PORT)
		setIfNotEnv("GMAIL_SMTP_HOST", cfg.GMAIL_SMTP_HOST)
		setIfNotEnv("GMAIL_SMTP_PORT", cfg.GMAIL_SMTP_PORT)
		setIfNotEnv("GMAIL_ALLOW_SEND", cfg.GMAIL_ALLOW_SEND)
		setIfNotEnv("GMAIL_ALLOW_RECEIVE", cfg.GMAIL_ALLOW_RECEIVE)
	}

	log.Println("Starting Oido Gmail MCP Server v1.0.0...")
	RunMCPServer()
}
