// Package service installs and manages the Drift binary as an OS service via
// github.com/kardianos/service: systemd user unit on Linux, launchd plist on
// macOS, Windows Service on Windows. One Go API, three OS implementations.
package service
