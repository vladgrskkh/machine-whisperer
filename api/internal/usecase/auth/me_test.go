package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth/authmock"
)

func TestMe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		lookupErr error
		errorType string
	}{
		{name: "existing"},
		{name: "missing", lookupErr: user.ErrNotFound},
		{name: "database", lookupErr: errors.New("private database error detail"), errorType: "internal"},
		{name: "canceled", lookupErr: context.Canceled, errorType: "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
			tracer := provider.Tracer("test")
			ctx, parent := tracer.Start(t.Context(), "request")
			defer parent.End()
			if errors.Is(tc.lookupErr, context.Canceled) {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			repository := authmock.NewMeRepository(t)
			expected := user.User{
				ID:        userID,
				Email:     loginEmail,
				CreatedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
			}
			var repositorySpan trace.SpanContext
			repository.EXPECT().FindByID(mock.Anything, userID).
				Run(func(ctx context.Context, _ string) {
					repositorySpan = trace.SpanContextFromContext(ctx)
					if errors.Is(tc.lookupErr, context.Canceled) {
						assert.ErrorIs(t, ctx.Err(), context.Canceled)
					}
				}).
				Return(expected, tc.lookupErr).Once()
			actual, err := auth.NewMe(repository, tracer).Me(ctx, userID)
			switch {
			case tc.lookupErr == nil:
				assert.NoError(t, err)
				assert.Equal(t, expected, actual)
			case errors.Is(tc.lookupErr, user.ErrNotFound):
				assert.ErrorIs(t, err, auth.ErrAuthenticationFailed)
				assert.Empty(t, actual)
			default:
				assert.ErrorIs(t, err, tc.lookupErr)
				assert.NotErrorIs(t, err, auth.ErrAuthenticationFailed)
				assert.Empty(t, actual)
			}

			spans := recorder.Ended()
			require.Len(t, spans, 1)
			span := spans[0]
			assert.Equal(t, "auth.me", span.Name())
			assert.Equal(t, parent.SpanContext(), span.Parent())
			assert.Equal(t, span.SpanContext(), repositorySpan)
			assert.Empty(t, span.Status().Description)
			assert.Empty(t, span.Events())
			if tc.errorType == "" {
				assert.Equal(t, codes.Unset, span.Status().Code)
				assert.Empty(t, span.Attributes())
			} else {
				assert.Equal(t, codes.Error, span.Status().Code)
				attrs := span.Attributes()
				require.Len(t, attrs, 1)
				assert.Equal(t, "error.type", string(attrs[0].Key))
				assert.Equal(t, tc.errorType, attrs[0].Value.AsString())
			}
		})
	}
}
