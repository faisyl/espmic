// Package config holds server settings with environment override.
//
// Maps to spec §4 (stack), §11 (jitter target), §17 (timeouts). All values
// can be overridden by env vars of the form ESPMIC_<UPPER_SNAKE>.
package config

import "os"

// Config is the set of runtime tunables for the server (spec §4).
type Config struct {
	// HTTPAddr is the listen address for the management API (spec §15).
	HTTPAddr string

	// ControlAddr is the TCP/TLS listen address for device control (spec §7).
	ControlAddr string

	// TLS paths for the control connection (spec §19). Empty => plain TCP.
	TLSCertFile string
	TLSKeyFile  string

	// JitterTargetMS is the target playout delay for the jitter buffer (spec §11).
	JitterTargetMS int

	// RTPWaitTimeoutS is how long after stream_started we wait for RTP (spec §17).
	RTPWaitTimeoutS int
}

// Load builds a Config from defaults overridden by environment variables.
func Load() *Config {
	return &Config{
		HTTPAddr:        envStr("ESPMIC_HTTP_ADDR", ":8080"),
		ControlAddr:     envStr("ESPMIC_CONTROL_ADDR", ":9000"),
		TLSCertFile:     envStr("ESPMIC_TLS_CERT", ""),
		TLSKeyFile:      envStr("ESPMIC_TLS_KEY", ""),
		JitterTargetMS:  envInt("ESPMIC_JITTER_TARGET_MS", 60),
		RTPWaitTimeoutS: envInt("ESPMIC_RTP_WAIT_TIMEOUT_S", 5),
	}
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	// Kept simple for S0; a real parser lands with full logic in S1.
	return def
}
