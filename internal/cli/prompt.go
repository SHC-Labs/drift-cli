package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// isInteractive returns true when stdin is a real terminal that can
// accept the wizard's prompts. /dev/null and pipe redirection both
// look like char devices to ModeCharDevice but neither is a real TTY,
// so a stricter ioctl-based check (via golang.org/x/term) is the only
// way to tell them apart on Linux.
//
// The install one-liner uses `bash <(curl ...)` (process substitution)
// which preserves the parent shell's TTY, OR `curl | sh` which
// doesn't. install.sh handles the pipe form by re-attaching /dev/tty
// before exec; this check tolerates either path because the redirect
// makes stdin point at the actual terminal.
func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// promptString prints label, reads a line from r, returns the trimmed
// answer or def when the user just hits enter. Caller chooses the
// reader so tests can inject deterministic input.
func promptString(w io.Writer, r *bufio.Reader, label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(w, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(w, "%s: ", label)
	}
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// promptYesNo accepts y/yes/n/no (case-insensitive). Empty input
// returns def. Anything else re-prompts (caps at 3 retries before
// returning def to avoid livelock on weird input streams).
func promptYesNo(w io.Writer, r *bufio.Reader, label string, def bool) (bool, error) {
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	for retry := 0; retry < 3; retry++ {
		fmt.Fprintf(w, "%s %s: ", label, suffix)
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return def, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(w, "  please answer y or n")
	}
	return def, nil
}

// section prints a numbered wizard step header. Keeps the visual
// rhythm consistent across steps without pulling in a TUI library.
func section(w io.Writer, n, total int, title string) {
	fmt.Fprintf(w, "\n[%d/%d] %s\n", n, total, title)
}
