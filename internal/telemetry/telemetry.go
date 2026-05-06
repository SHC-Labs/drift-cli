// Package telemetry holds the opt-out state for the four install
// state events. Honors DRIFT_NO_TELEMETRY=1 env (per-process kill
// switch) AND ~/.drift/config.json's Telemetry field (persistent
// preference set by drift telemetry off / on).
//
// v1 only emits the four state events documented in PRIVACY.md;
// future telemetry additions get gated by the same Enabled() check.
package telemetry

import (
	"os"

	"github.com/SHC-Labs/drift/internal/config"
)

// EnvKillSwitch is the env var that disables ALL telemetry for the
// current process. Honored regardless of the persisted preference.
const EnvKillSwitch = "DRIFT_NO_TELEMETRY"

// Enabled returns true if telemetry should fire. Order of resolution:
//  1. DRIFT_NO_TELEMETRY env var: any non-empty value disables.
//  2. ~/.drift/config.json Telemetry field: "off" disables, anything
//     else (including empty) is treated as enabled.
//
// Default is enabled per PRIVACY.md (the four state events drive the
// dashboard's Getting Started checkoff; users opt out explicitly).
func Enabled() bool {
	if v := os.Getenv(EnvKillSwitch); v != "" {
		return false
	}
	cfg, err := config.ReadBinaryConfig()
	if err != nil {
		// Read failure means we don't know the preference. Default to
		// enabled so install events fire on first run; persist a
		// preference via drift telemetry on/off if the user cares.
		return true
	}
	return cfg.Telemetry != "off"
}

// SetEnabled writes the preference to ~/.drift/config.json. Idempotent.
func SetEnabled(enabled bool) error {
	cfg, err := config.ReadBinaryConfig()
	if err != nil {
		return err
	}
	if enabled {
		cfg.Telemetry = "on"
	} else {
		cfg.Telemetry = "off"
	}
	return config.WriteBinaryConfig(cfg)
}
