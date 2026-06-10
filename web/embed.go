// Package web embeds the static frontend assets served by unbound-dash.
package web

import "embed"

//go:embed all:static
var Static embed.FS
