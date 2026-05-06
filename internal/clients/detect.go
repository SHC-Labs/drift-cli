package clients

import (
	"os"
	"path/filepath"
	"runtime"
)

// ClientID is the canonical name for each supported MCP client. Same
// vocabulary as the API spec's client-connected enum so state events
// can pass these through unmodified.
type ClientID string

const (
	ClaudeCode  ClientID = "claude-code"
	Cursor      ClientID = "cursor"
	Windsurf    ClientID = "windsurf"
	Antigravity ClientID = "antigravity"
	Zed         ClientID = "zed"
	Kimi        ClientID = "kimi"
	ChatGPT     ClientID = "chatgpt"
	VSCode      ClientID = "vscode"
	Kilo        ClientID = "kilo"
)

// AllClientIDs is the canonical iteration order. Used by drift install
// to detect every supported client and write the right config for each.
var AllClientIDs = []ClientID{
	ClaudeCode, Cursor, Windsurf, Antigravity, Zed, Kimi, ChatGPT, VSCode, Kilo,
}

// Detected describes one client that drift install found on this
// machine. The Writer field handles per-client config writes; nil
// Writer means the client is "manual setup only" (e.g. ChatGPT
// desktop's settings UI has no scriptable config).
type Detected struct {
	ID          ClientID
	ConfigPath  string // where drift would write (or empty for manual-only)
	HooksAware  bool   // true if the client supports auto-firing hooks (Claude Code only in v1)
	WriterErr   error  // set when DetectAll caught an error inspecting this client
}

// DetectAll scans the system for every supported MCP client and
// returns one Detected per client found. Order matches AllClientIDs.
// Cheap: just stats well-known paths, no shelling out.
func DetectAll() []Detected {
	var out []Detected
	if d := detectClaudeCode(); d != nil {
		out = append(out, *d)
	}
	if d := detectCursor(); d != nil {
		out = append(out, *d)
	}
	if d := detectWindsurf(); d != nil {
		out = append(out, *d)
	}
	if d := detectAntigravity(); d != nil {
		out = append(out, *d)
	}
	if d := detectZed(); d != nil {
		out = append(out, *d)
	}
	if d := detectKimi(); d != nil {
		out = append(out, *d)
	}
	if d := detectVSCode(); d != nil {
		out = append(out, *d)
	}
	if d := detectKilo(); d != nil {
		out = append(out, *d)
	}
	// ChatGPT desktop has no scriptable config; we don't auto-detect.
	// Customers running ChatGPT see the manual-paste instructions in
	// the install output instead.
	return out
}

// homeDir returns os.UserHomeDir() or empty on error. Used by every
// detect* function so we don't propagate error handling for the
// trivially-recoverable "no $HOME" case.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func detectClaudeCode() *Detected {
	h := homeDir()
	if h == "" {
		return nil
	}
	dir := filepath.Join(h, ".claude")
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil
	}
	return &Detected{
		ID:         ClaudeCode,
		ConfigPath: filepath.Join(dir, "settings.json"),
		HooksAware: true,
	}
}

func detectCursor() *Detected {
	h := homeDir()
	if h == "" {
		return nil
	}
	// Cursor has both a global config dir AND per-project config. We
	// detect the global app dir presence as the install signal.
	for _, candidate := range cursorCandidatePaths(h) {
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return &Detected{
				ID:         Cursor,
				ConfigPath: filepath.Join(candidate, "User", "globalStorage"),
				HooksAware: false,
			}
		}
	}
	return nil
}

func cursorCandidatePaths(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Cursor")}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return nil
		}
		return []string{filepath.Join(appData, "Cursor")}
	default: // linux, freebsd, etc
		return []string{
			filepath.Join(home, ".config", "Cursor"),
			filepath.Join(home, ".config", "cursor"),
		}
	}
}

func detectWindsurf() *Detected {
	h := homeDir()
	if h == "" {
		return nil
	}
	// Windsurf uses ~/.codeium/windsurf/ for MCP config.
	dir := filepath.Join(h, ".codeium", "windsurf")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return &Detected{
			ID:         Windsurf,
			ConfigPath: filepath.Join(dir, "mcp_config.json"),
			HooksAware: false,
		}
	}
	return nil
}

func detectAntigravity() *Detected {
	h := homeDir()
	if h == "" {
		return nil
	}
	// Google Antigravity stores MCP config under ~/.gemini/antigravity/.
	dir := filepath.Join(h, ".gemini", "antigravity")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return &Detected{
			ID:         Antigravity,
			ConfigPath: filepath.Join(dir, "mcp_config.json"),
			HooksAware: false,
		}
	}
	return nil
}

func detectZed() *Detected {
	h := homeDir()
	if h == "" {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		dir := filepath.Join(h, ".config", "zed")
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return &Detected{
				ID:         Zed,
				ConfigPath: filepath.Join(dir, "settings.json"),
				HooksAware: false,
			}
		}
	default:
		dir := filepath.Join(h, ".config", "zed")
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return &Detected{
				ID:         Zed,
				ConfigPath: filepath.Join(dir, "settings.json"),
				HooksAware: false,
			}
		}
	}
	return nil
}

func detectKimi() *Detected {
	h := homeDir()
	if h == "" {
		return nil
	}
	dir := filepath.Join(h, ".kimi")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return &Detected{
			ID:         Kimi,
			ConfigPath: filepath.Join(dir, "mcp.json"),
			HooksAware: false,
		}
	}
	return nil
}

func detectVSCode() *Detected {
	h := homeDir()
	if h == "" {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		dir := filepath.Join(h, "Library", "Application Support", "Code")
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return &Detected{
				ID:         VSCode,
				ConfigPath: filepath.Join(dir, "User", "settings.json"),
				HooksAware: false,
			}
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData != "" {
			dir := filepath.Join(appData, "Code")
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				return &Detected{
					ID:         VSCode,
					ConfigPath: filepath.Join(dir, "User", "settings.json"),
					HooksAware: false,
				}
			}
		}
	default:
		dir := filepath.Join(h, ".config", "Code")
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return &Detected{
				ID:         VSCode,
				ConfigPath: filepath.Join(dir, "User", "settings.json"),
				HooksAware: false,
			}
		}
	}
	return nil
}

func detectKilo() *Detected {
	h := homeDir()
	if h == "" {
		return nil
	}
	dir := filepath.Join(h, ".config", "kilo")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return &Detected{
			ID:         Kilo,
			ConfigPath: filepath.Join(dir, "kilo.jsonc"),
			HooksAware: false,
		}
	}
	return nil
}
