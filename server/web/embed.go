// Package web holds the dashboard static assets (spec §3).
package web

import "embed"

//go:embed *.html
var Assets embed.FS
