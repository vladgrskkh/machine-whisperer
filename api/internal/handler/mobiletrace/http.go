package mobiletrace

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

const (
	ExportPath       = "/v1/mobile/traces"
	maxBodyBytes     = 32 * 1024
	maxHeaderBytes   = 4 * 1024
	maxBatchRequests = 12
	maxSubjects      = 4096
	maxConcurrent    = 8
	forwardTimeout   = time.Second
	protobufType     = "application/x-protobuf"
)

// Forward sends a validated batch to the private Collector.
type Forward func(context.Context, *coltrace.ExportTraceServiceRequest) error

type Handler struct {
	key     []byte
	now     func() time.Time
	forward Forward
	active  chan struct{}

	mu      sync.Mutex
	window  time.Time
	batches map[string]int
}

func New(key []byte, forward Forward) (*Handler, error) {
	if len(key) != 32 || forward == nil {
		return nil, errors.New("invalid mobile trace configuration")
	}
	return &Handler{
		key: append([]byte(nil), key...), now: time.Now, forward: forward, active: make(chan struct{}, maxConcurrent),
	}, nil
}

func (h *Handler) History(next http.Handler) http.Handler {
	if h == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := middleware.UserID(r.Context())
		span := trace.SpanContextFromContext(r.Context())
		if ok && r.Header.Get("X-Mw-Trace") == "1" && span.IsValid() && span.IsSampled() {
			w.Header().Set("X-Mw-Traceparent", "00-"+span.TraceID().String()+"-"+span.SpanID().String()+"-01")
			w.Header().Set("X-Mw-Trace-Receipt", h.makeReceipt(user, span))
		}
		next.ServeHTTP(w, r)
	})
}

// ServeHTTP accepts a bounded OTLP/HTTP binary protobuf trace batch.
//
// @Summary Submit mobile trace spans
// @Description Accepts an uncompressed OTLP binary trace batch of at most 32 KiB and 16 spans.
// @Tags operational
// @Security UserAccessJWT
// @Accept application/x-protobuf
// @Produce application/x-protobuf,application/json
// @Param batch body string true "Binary OTLP ExportTraceServiceRequest" Format(binary)
// @Param X-Mw-Trace-Receipts header string false "Comma-separated trace receipts"
// @Success 200 {file} file "Empty binary OTLP ExportTraceServiceResponse (zero bytes)"
// @Failure 400 {object} ErrorResponse "Invalid trace batch (JSON)"
// @Failure 401 {object} ErrorResponse "Authentication failed (JSON)"
// @Failure 413 {object} ErrorResponse "Trace batch exceeds 32 KiB (JSON)"
// @Failure 415 {object} ErrorResponse "Unsupported encoding (JSON)"
// @Failure 429 {object} ErrorResponse "Export rate exceeded (JSON)"
// @Failure 503 {object} ErrorResponse "Trace export unavailable (JSON)"
// @Router /v1/mobile/traces [post]
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserID(r.Context())
	if !ok {
		middleware.Unauthorized(w)
		return
	}
	if r.Header.Get("Content-Type") != protobufType || r.Header.Get("Content-Encoding") != "" {
		handler.Error(w, http.StatusUnsupportedMediaType, "invalid_trace_encoding", "Expected OTLP protobuf")
		return
	}
	if !h.allow(user) {
		handler.Error(w, http.StatusTooManyRequests, "trace_rate_limit", "Trace export rate exceeded")
		return
	}
	select {
	case h.active <- struct{}{}:
		defer func() { <-h.active }()
	default:
		handler.Error(w, http.StatusServiceUnavailable, "trace_unavailable", "Trace export unavailable")
		return
	}

	input := new(coltrace.ExportTraceServiceRequest)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		handler.Error(w, http.StatusRequestEntityTooLarge, "trace_batch_too_large", "Trace batch too large")
		return
	}
	receipts := r.Header.Get("X-Mw-Trace-Receipts")
	if err != nil || len(receipts) > maxHeaderBytes ||
		(proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, input) != nil {
		handler.Error(w, http.StatusBadRequest, "invalid_trace_batch", "Invalid trace batch")
		return
	}
	batch, err := h.sanitize(input, user, receipts)
	if err != nil {
		handler.Error(w, http.StatusBadRequest, "invalid_trace_batch", "Invalid trace batch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), forwardTimeout)
	defer cancel()
	if h.forward(ctx, batch) != nil {
		handler.Error(w, http.StatusServiceUnavailable, "trace_unavailable", "Trace export unavailable")
		return
	}
	w.Header().Set("Content-Type", protobufType)
	// An empty OTLP response encodes to zero bytes.
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) allow(user string) bool {
	window := h.now().Truncate(time.Minute)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.window != window {
		h.window = window
		h.batches = make(map[string]int)
	}
	count, known := h.batches[user]
	if count >= maxBatchRequests || (!known && len(h.batches) >= maxSubjects) {
		return false
	}
	h.batches[user]++
	return true
}
