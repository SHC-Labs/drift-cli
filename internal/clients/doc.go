// Package clients detects installed MCP clients (Claude Code, Cursor,
// Windsurf, Antigravity, Zed, Kimi, ChatGPT desktop, VS Code, Kilo) and
// writes the right config for each. Conservative writers: read existing
// config, modify only fields drift owns, write atomically. Never overwrite
// unrelated fields.
package clients
