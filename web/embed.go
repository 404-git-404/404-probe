package web

import "embed"

// Files contains the dashboard assets served by the single server binary.
//
//go:embed static/*
var Files embed.FS
