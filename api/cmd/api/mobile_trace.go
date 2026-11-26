package main

import (
	"encoding/hex"
	"log/slog"
	"os"
	"strings"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/mobiletrace"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/tracing"
)

func mobileTraceHandler(logger *slog.Logger) (*mobiletrace.Handler, func()) {
	keyFile := os.Getenv("MOBILE_TRACE_SECRET_FILE")
	if keyFile == "" {
		return nil, func() {}
	}
	material, err := os.ReadFile(keyFile) //nolint:gosec // The operator sets the local secret file.
	if err != nil {
		logger.Warn("mobile tracing disabled: receipt secret unavailable")
		return nil, func() {}
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(material)))
	if err != nil || len(key) != 32 {
		logger.Warn("mobile tracing disabled: invalid receipt secret")
		return nil, func() {}
	}
	export, closeClient, err := tracing.MobileForwarder()
	if err != nil {
		logger.Warn("mobile tracing disabled: OTLP forwarder unavailable")
		return nil, func() {}
	}
	handler, err := mobiletrace.New(key, export)
	if err != nil {
		_ = closeClient()
		logger.Warn("mobile tracing disabled: invalid handler configuration")
		return nil, func() {}
	}
	return handler, func() { _ = closeClient() }
}
