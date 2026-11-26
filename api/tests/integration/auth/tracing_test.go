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
	pool := testutil.Postgres(t).Config()
	cfg := testfixture.AuthConfig(t)
	cfg.Database.URL = pool.ConnString()
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

	seen := len(recorder.Ended())
	for index, status := range []int{http.StatusCreated, http.StatusConflict} {
		response, err := server.Client().Post(server.URL+"/v1/auth/signup", "application/json",
			strings.NewReader(`{"email":"private@example.com","password":"private signup password"}`))
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		<-handled
		require.Equal(t, status, response.StatusCode)

		ended := recorder.Ended()
		spans := ended[seen:]
		seen = len(ended)
		query, prepare := signupDatabaseSpans(t, spans)
		signup := findSpanByName(t, spans, "auth.signup")
		request := findSpanByName(t, spans, "POST /v1/auth/signup")
		assert.Equal(t, trace.SpanKindClient, query.SpanKind())
		assert.Equal(t, signup.SpanContext(), query.Parent())
		assert.Equal(t, request.SpanContext(), signup.Parent())
		assert.Equal(t, codes.Unset, signup.Status().Code)
		assert.Equal(t, "github.com/exaring/otelpgx", query.InstrumentationScope().Name)
		if index == 0 {
			require.NotNil(t, prepare)
			assert.Equal(t, query.SpanContext(), prepare.Parent())
		}
		attrs := databaseSpanAttributes(query)
		assert.Equal(t, "postgresql", attrs["db.system.name"])
		assert.Equal(t, "INSERT", attrs["db.operation.name"])
		assert.Equal(t, pool.ConnConfig.Host, attrs["server.address"])
		assert.Equal(t, int64(pool.ConnConfig.Port), attrs["server.port"])
		assert.Equal(t, pool.ConnConfig.Database, attrs["db.namespace"])
		assert.Equal(t, pool.ConnConfig.User, attrs["user.name"])
		assert.NotContains(t, attrs, "pgx.query.parameters")
		if status == http.StatusConflict {
			assert.Equal(t, codes.Error, query.Status().Code)
			assert.Equal(t, "23505", attrs["pgx.sql_state"])
		} else {
			assert.Equal(t, codes.Unset, query.Status().Code)
			assert.Equal(t, int64(1), attrs["pgx.rows_affected"])
		}
	}
}

func signupDatabaseSpans(t *testing.T, spans []sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, sdktrace.ReadOnlySpan) {
	t.Helper()
	const signupSQL = "INSERT INTO users (id,email,password_hash) VALUES ($1,$2,$3) RETURNING id::text,email,created_at"
	var query, prepare sdktrace.ReadOnlySpan
	for _, span := range spans {
		attrs := databaseSpanAttributes(span)
		if attrs["db.query.text"] != signupSQL {
			continue
		}
		if _, prepared := attrs["pgx.prepare_stmt.name"]; prepared {
			prepare = span
		} else {
			query = span
		}
	}
	require.NotNil(t, query, "signup query span with bound placeholders")
	return query, prepare
}

func findSpanByName(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("missing %s span", name)
	return nil
}

func databaseSpanAttributes(span sdktrace.ReadOnlySpan) map[string]any {
	attrs := make(map[string]any)
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsInterface()
	}
	return attrs
}
