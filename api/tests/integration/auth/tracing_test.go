//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestApplicationTracesPostgresInSignup(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	cfg := testfixture.AuthConfig(t)
	cfg.Database.URL = testutil.Postgres(t).Config().ConnString()
	app, err := application.New(t.Context(), cfg,
		application.WithDatabase(cfg.Database),
		application.WithTracing(provider, propagation.TraceContext{}),
	)
	require.NoError(t, err)
	t.Cleanup(app.Close)
	routes := app.Routes()
	handled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.ServeHTTP(w, r)
		handled <- struct{}{}
	}))
	t.Cleanup(server.Close)

	for index, status := range []int{http.StatusCreated, http.StatusConflict} {
		response, err := server.Client().Post(server.URL+"/v1/auth/signup", "application/json",
			strings.NewReader(`{"email":"private@example.com","password":"private signup password"}`))
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		<-handled
		require.Equal(t, status, response.StatusCode)

		spans := recorder.Ended()
		require.Len(t, spans, (index+1)*3)
		query, signup, request := spans[index*3], spans[index*3+1], spans[index*3+2]
		assert.Equal(t, "INSERT", query.Name())
		assert.Equal(t, trace.SpanKindClient, query.SpanKind())
		assert.Equal(t, signup.SpanContext(), query.Parent())
		assert.Equal(t, "auth.signup", signup.Name())
		assert.Equal(t, request.SpanContext(), signup.Parent())
		assert.Equal(t, codes.Unset, signup.Status().Code)
		assert.Equal(t, "POST /v1/auth/signup", request.Name())
		assert.Empty(t, query.Events())
		assert.Empty(t, query.Status().Description)
		want := map[string]any{"db.system.name": "postgresql", "db.operation.name": "INSERT"}
		if status == http.StatusConflict {
			want["error.type"] = "database_error"
			assert.Equal(t, codes.Error, query.Status().Code)
		} else {
			assert.Equal(t, codes.Unset, query.Status().Code)
		}
		got := make(map[string]any)
		for _, attr := range query.Attributes() {
			got[string(attr.Key)] = attr.Value.AsInterface()
		}
		assert.Equal(t, want, got)
	}
}
