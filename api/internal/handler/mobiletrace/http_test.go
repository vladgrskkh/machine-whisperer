package mobiletrace_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/mobiletrace"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
)

func TestAuthenticatedHistoryReceiptLinksExport(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	jwt, err := token.NewJWT(token.Keys{
		SigningKeyID: "test", SigningKey: private, VerifyKeys: map[string]ed25519.PublicKey{"test": public},
	}, func() time.Time { return now })
	require.NoError(t, err)
	bearer, err := jwt.Sign(t.Context(), session.AccessClaims{
		UserID: uuid.NewString(), SessionID: uuid.NewString(), JTI: uuid.NewString(),
		IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	})
	require.NoError(t, err)

	forwarded := make(chan *coltrace.ExportTraceServiceRequest, 1)
	h, err := mobiletrace.New(
		bytes.Repeat([]byte{42}, 32),
		func(_ context.Context, batch *coltrace.ExportTraceServiceRequest) error {
			forwarded <- batch
			return nil
		},
	)
	require.NoError(t, err)
	serverTrace := trace.TraceID{1}
	serverSpan := trace.SpanID{2}
	serverContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: serverTrace, SpanID: serverSpan, TraceFlags: trace.FlagsSampled,
	})
	router := http.NewServeMux()
	history := h.History(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	router.Handle("GET /history", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history.ServeHTTP(w, r.WithContext(trace.ContextWithSpanContext(r.Context(), serverContext)))
	}))
	router.Handle("POST "+mobiletrace.ExportPath, h)
	server := httptest.NewServer(middleware.AccessToken(jwt)(router))
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/history", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("X-Mw-Trace", "1")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "00-"+serverTrace.String()+"-"+serverSpan.String()+"-01", response.Header.Get("X-Mw-Traceparent"))
	receipt := response.Header.Get("X-Mw-Trace-Receipt")
	require.NotEmpty(t, receipt)

	traceID, spanID := trace.TraceID{3}, trace.SpanID{4}
	timestamp := uint64(now.UnixNano())
	input := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{
				Name:              "machine.history.load",
				Kind:              tracepb.Span_SPAN_KIND_INTERNAL,
				TraceId:           traceID[:],
				SpanId:            spanID[:],
				StartTimeUnixNano: timestamp,
				EndTimeUnixNano:   timestamp,
			},
			{
				Name:              "GET /v1/machines/{machine_id}/telemetry",
				Kind:              tracepb.Span_SPAN_KIND_CLIENT,
				TraceId:           traceID[:],
				SpanId:            []byte{5, 0, 0, 0, 0, 0, 0, 0},
				ParentSpanId:      spanID[:],
				StartTimeUnixNano: timestamp,
				EndTimeUnixNano:   timestamp,
				Links:             []*tracepb.Span_Link{{TraceId: serverTrace[:], SpanId: serverSpan[:]}},
			},
		}}},
	}}}
	payload, err := proto.Marshal(input)
	require.NoError(t, err)
	for _, authenticated := range []bool{false, true} {
		request, err = http.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			server.URL+mobiletrace.ExportPath,
			bytes.NewReader(payload),
		)
		require.NoError(t, err)
		if authenticated {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		request.Header.Set("Content-Type", "application/x-protobuf")
		request.Header.Set("X-Mw-Trace-Receipts", receipt)
		response, err = server.Client().Do(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		if authenticated {
			require.Equal(t, http.StatusOK, response.StatusCode)
		} else {
			require.Equal(t, http.StatusUnauthorized, response.StatusCode)
		}
	}
	select {
	case batch := <-forwarded:
		spans := batch.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()
		assert.NotEqual(t, traceID[:], spans[1].GetTraceId())
		require.Len(t, spans[1].GetLinks(), 1)
		assert.Equal(t, serverTrace[:], spans[1].GetLinks()[0].GetTraceId())
		assert.Equal(t, serverSpan[:], spans[1].GetLinks()[0].GetSpanId())
	default:
		t.Fatal("authenticated trace batch did not reach the forwarder")
	}
}
