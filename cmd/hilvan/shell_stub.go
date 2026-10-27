//go:build !linux && !windows && !darwin

package main

import (
	"github.com/rs/zerolog"

	"hilvan/internal/core"
	"hilvan/internal/server"
)

func shellAvailable() bool { return false }

func shellInit(args []string) bool { return false }

func runShell(m *core.Manager, srv *server.Server, log zerolog.Logger, dev bool, openURI string) {}
