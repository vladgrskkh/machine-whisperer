package application_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
)

func TestLoggerCorrelatesSampledSpanWithServiceFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		env         string
		expectedEnv string
		sampler     sdktrace.Sampler
	}{
		{name: "sampled", env: "test", expectedEnv: "test", sampler: sdktrace.AlwaysSample()},
		{name: "unsampled", expectedEnv: "dev", sampler: sdktrace.NeverSample()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			logger := application.NewLogger(&out, tc.env, slog.LevelInfo).With("field", "value")
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(tc.sampler))
			ctx, span := provider.Tracer("test").Start(t.Context(), "example")
			defer span.End()
			logger.InfoContext(ctx, "test event")

			var fields map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &fields))
			assert.Equal(t, "api", fields["app"])
			assert.Equal(t, tc.expectedEnv, fields["env"])
			assert.Equal(t, "value", fields["field"])
			if span.SpanContext().IsSampled() {
				assert.Equal(t, span.SpanContext().TraceID().String(), fields["trace_id"])
				assert.Equal(t, span.SpanContext().SpanID().String(), fields["span_id"])
			} else {
				assert.NotContains(t, fields, "trace_id")
				assert.NotContains(t, fields, "span_id")
			}
		})
	}
}
