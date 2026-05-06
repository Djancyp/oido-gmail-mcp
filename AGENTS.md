# Oido Gmail — Agent Guide

## Commands

| Action | Command |
|--------|---------|
| Build | `make build` (CGO_ENABLED=0) |
| Package for plugin UI | `make dist` — creates `dist/oido-gmail.zip` |
| Clean | `make clean` |
| Single binary | `go build -o oido-gmail-mcp .` |

No test/lint config exists. `go vet ./...` before commit. No `.golangci.yml`.

## Structure

Single `package main` — `main.go`, `mcp_server.go`, `gmail.go`. No internal packages.

## Key Gotchas

- **Local dev**: `go.mod` has `replace github.com/Djancyp/oido-studio => ../../app`. Requires sibling `app/` dir at repo root. Build will fail without it.
- **`.env` loading**: `loadDotEnv()` reads `.env` from **binary's directory** (via `os.Executable()`), NOT `CWD`. `.env` is gitignored.
- **`--config` flag**: Accepts JSON string for settings. Precedence: OS env var > `--config` JSON > `.env` file. Example: `./oido-gmail-mcp --config '{"GMAIL_EMAIL":"x@y.com","GMAIL_PASSWORD":"pass"}'`
- **Connection**: `TestConnection()` logs FAILURE but does not abort. Server starts regardless.
- **SMTP**: Port 587 uses STARTTLS, port 465 uses implicit TLS (SMTPS). Handled automatically.
- **Defaults**: `GMAIL_ALLOW_SEND=false`, `GMAIL_ALLOW_RECEIVE=true` — send/draft blocked unless explicitly enabled.
- **Commands/ dir**: TOML prompt templates for AI orchestration, bundled in `make dist`. Not used at runtime.
- **`OIDO.md`**: Agent context doc embedded in dist. Describes 5 MCP tools + usage examples.
- **No race tests**: No `_test.go` files. No `-race` verification anywhere.
- **CI**: GitHub Actions release on `v*` tag or `workflow_dispatch`. Cross-compiles linux+darwin amd64+arm64 (no linux/arm64).
