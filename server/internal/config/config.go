// Package config holds server settings with environment override.
//
// Maps to spec §4 (stack), §11 (jitter target), §17 (timeouts). All values
// can be overridden by env vars of the form ESPMIC_<UPPER_SNAKE>.
package config

import (
	"os"
	"strconv"
)

// Config is the set of runtime tunables for the server (spec §4).
type Config struct {
	// HTTPAddr is the listen address for the management API (spec §15).
	HTTPAddr string

	// ControlAddr is the TCP/TLS listen address for device control (spec §7).
	ControlAddr string

	// TLS paths for the control connection (spec §19). Empty => plain TCP.
	TLSCertFile string
	TLSKeyFile  string

	// DeviceCredential is an optional shared secret for device enrollment.
	// Empty = open enrollment (LAN default, TOFU). Non-empty = credential
	// must match this value (constant-time compare) before device is
	// accepted. Set via ESPMIC_DEVICE_CREDENTIAL (spec §19).
	DeviceCredential string

	// LogLevel controls server log verbosity. "info" (default) or "debug".
	// Set via ESPMIC_LOG_LEVEL (case-insensitive).
	LogLevel string

	// JitterTargetMS is the target playout delay for the jitter buffer (spec §11).
	JitterTargetMS int

	// RTPWaitTimeoutS is how long after stream_started we wait for RTP (spec §17).
	RTPWaitTimeoutS int

	// DBPath is the SQLite database path (spec §20).
	DBPath string

	// RecordingsDir is the directory where recording files are stored.
	RecordingsDir string

	// RTPBindPort is the UDP port the RTP receiver binds to. 0 = dynamic
	// (current behavior; default). Set via ESPMIC_RTP_BIND_PORT.
	RTPBindPort int

	// AdvertiseHost overrides the RTP destination host advertised to the
	// device. Empty = derive from the control connection (current behavior;
	// default). Set via ESPMIC_ADVERTISE_HOST. Use this when the server is
	// behind Docker/NAT so the device gets a reachable host.
	AdvertiseHost string

	// AdvertiseRTPPort overrides the RTP destination port advertised to the
	// device. 0 = advertise the actually-bound port (current behavior;
	// default). Set via ESPMIC_ADVERTISE_RTP_PORT.
	AdvertiseRTPPort int

	// RTPDisappearTimeoutS is the ACTIVE silence window before the server
	// fails the stream as RTP_TIMEOUT (spec §17). Default 3s (was hardcoded
	// 1s, too aggressive for real WiFi). Set via ESPMIC_RTP_DISAPPEAR_TIMEOUT_S.
	RTPDisappearTimeoutS int
}

// Load builds a Config from defaults overridden by environment variables.
func Load() *Config {
	return &Config{
		HTTPAddr:             envStr("ESPMIC_HTTP_ADDR", ":8080"),
		ControlAddr:          envStr("ESPMIC_CONTROL_ADDR", ":9000"),
		TLSCertFile:          envStr("ESPMIC_TLS_CERT", ""),
		TLSKeyFile:           envStr("ESPMIC_TLS_KEY", ""),
		DeviceCredential:     envStr("ESPMIC_DEVICE_CREDENTIAL", ""),
		LogLevel:             envStr("ESPMIC_LOG_LEVEL", "info"),
		JitterTargetMS:       envInt("ESPMIC_JITTER_TARGET_MS", 60),
		RTPWaitTimeoutS:      envInt("ESPMIC_RTP_WAIT_TIMEOUT_S", 5),
		DBPath:               envStr("ESPMIC_DB_PATH", "espmic.db"),
		RecordingsDir:        envStr("ESPMIC_RECORDINGS_DIR", "recordings"),
		RTPBindPort:          envInt("ESPMIC_RTP_BIND_PORT", 0),
		AdvertiseHost:        envStr("ESPMIC_ADVERTISE_HOST", ""),
		AdvertiseRTPPort:     envInt("ESPMIC_ADVERTISE_RTP_PORT", 0),
		RTPDisappearTimeoutS: envInt("ESPMIC_RTP_DISAPPEAR_TIMEOUT_S", 3),
	}
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	// strconv.Atoi; fall back to default on empty/error.
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}
