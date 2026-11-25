package clickhouse_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/metrics"
)

func TestListRecordsFailuresAfterQueryStarts(t *testing.T) {
	t.Parallel()
	registry := metrics.New()
	repository := clickhouse.NewRepository(failedQueryConn{}, registry)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancelDeadline := context.WithTimeout(t.Context(), 0)
	defer cancelDeadline()
	for _, ctx := range []context.Context{t.Context(), canceled, expired} {
		_, err := repository.List(ctx, domain.HistoryQuery{})
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	registry.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	body := response.Body.String()
	assert.Contains(t, body, "mw_clickhouse_operations_total{operation=\"list_history\",outcome=\"success\"} 0\n")
	for _, outcome := range []string{"error", "canceled", "timeout"} {
		assert.Contains(
			t,
			body,
			"mw_clickhouse_operations_total{operation=\"list_history\",outcome=\""+outcome+"\"} 1\n",
		)
	}
}

type failedQueryConn struct{ ch.Conn }

func (failedQueryConn) Query(context.Context, string, ...any) (driver.Rows, error) {
	return failingRows{}, nil
}

type failingRows struct{ driver.Rows }

func (failingRows) Next() bool   { return false }
func (failingRows) Err() error   { return io.ErrUnexpectedEOF }
func (failingRows) Close() error { return nil }
