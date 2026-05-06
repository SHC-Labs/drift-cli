package relay

import (
	"context"
	"time"

	"github.com/SHC-Labs/drift/internal/api"
	"github.com/SHC-Labs/drift/internal/keychain"
	"github.com/SHC-Labs/drift/internal/log"
	"github.com/SHC-Labs/drift/internal/telemetry"
)

// DefaultHeartbeatCadence is how often the relay reports liveness to
// the dashboard's /v1/install/relay-heartbeat endpoint. Server can
// override per-call via the X-Drift-Heartbeat-Cadence response header
// (e.g. throttle to every 5min during high load).
const DefaultHeartbeatCadence = 60 * time.Second

// MinHeartbeatCadence is the floor we honor for server-side cadence
// overrides. Stops a buggy server response from DDoSing itself.
const MinHeartbeatCadence = 10 * time.Second

// MaxHeartbeatCadence is the ceiling. If the server tries to set a
// 24-hour cadence, we cap it so the dashboard's "is relay alive?" UI
// stays accurate.
const MaxHeartbeatCadence = 30 * time.Minute

// RunHeartbeat fires the relay-heartbeat state event on a loop. Caller
// passes ctx tied to the relay's lifetime; goroutine exits cleanly on
// ctx cancel. Skips silently when the keychain has no token or no
// install_id (drift install not yet run, or running uninstalled).
//
// Spawned by service.Start as a sibling goroutine to the relay HTTP
// server. Failures are logged but don't kill the relay.
func RunHeartbeat(ctx context.Context, upstreamURL string) {
	startedAt := time.Now()
	cadence := DefaultHeartbeatCadence

	// First tick fires immediately so the dashboard sees the relay
	// come up without waiting a full minute. After that, regular
	// cadence applies.
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("relay.heartbeat", "stopping", nil)
			return
		case <-timer.C:
		}

		newCadence, err := tickHeartbeat(ctx, upstreamURL, time.Since(startedAt))
		if err != nil {
			log.Warn("relay.heartbeat", "tick_failed", map[string]any{
				"error": err.Error(),
			})
		} else if newCadence > 0 && newCadence != cadence {
			cadence = clampCadence(newCadence)
			log.Info("relay.heartbeat", "cadence_changed", map[string]any{
				"new_cadence_seconds": int(cadence.Seconds()),
			})
		}
		timer.Reset(cadence)
	}
}

// tickHeartbeat fires one heartbeat. Returns the server's preferred
// cadence (zero if no override) and any error. Errors don't stop the
// loop; the caller logs and retries on the next tick.
func tickHeartbeat(ctx context.Context, upstreamURL string, uptime time.Duration) (time.Duration, error) {
	if !telemetry.Enabled() {
		return 0, nil
	}
	token, err := keychain.GetToken()
	if err != nil || token == "" {
		// No token = nothing to authenticate with. Skip silently;
		// drift login or DRIFT_TOKEN= drift install will set one.
		return 0, nil
	}
	installID, err := keychain.GetInstallID()
	if err != nil || installID == "" {
		// No install_id = drift install hasn't run. Skip.
		return 0, nil
	}

	client := api.NewClient(upstreamURL, token)
	tickCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := client.PostRelayHeartbeat(tickCtx, api.RelayHeartbeatRequest{
		InstallID:     installID,
		UptimeSeconds: int64(uptime.Seconds()),
	})
	if err != nil {
		return 0, err
	}
	if resp != nil && resp.CadenceSeconds > 0 {
		return time.Duration(resp.CadenceSeconds) * time.Second, nil
	}
	return 0, nil
}

// clampCadence enforces [MinHeartbeatCadence, MaxHeartbeatCadence] on
// server-supplied cadence overrides.
func clampCadence(d time.Duration) time.Duration {
	if d < MinHeartbeatCadence {
		return MinHeartbeatCadence
	}
	if d > MaxHeartbeatCadence {
		return MaxHeartbeatCadence
	}
	return d
}

// FireRelayEnabled posts the relay-enabled state event once when the
// relay successfully binds and completes upstream handshake. Called
// from RunHeartbeat's first tick path so it fires AFTER bind + before
// the regular heartbeat loop.
func FireRelayEnabled(ctx context.Context, upstreamURL, transport, portOrPath string) {
	if !telemetry.Enabled() {
		return
	}
	token, err := keychain.GetToken()
	if err != nil || token == "" {
		return
	}
	installID, err := keychain.GetInstallID()
	if err != nil || installID == "" {
		return
	}
	client := api.NewClient(upstreamURL, token)
	parent, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	_ = api.PostWithRetry(parent, func(ctx context.Context) error {
		return client.PostRelayEnabled(ctx, api.RelayEnabledRequest{
			InstallID:  installID,
			Transport:  transport,
			PortOrPath: portOrPath,
		})
	})
}
