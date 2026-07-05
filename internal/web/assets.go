// Package web embeds the built frontend (frontend/dist, copied here by the
// Makefile's frontend target) into the server binary.
package web

import "embed"

//go:embed all:dist
var Files embed.FS
