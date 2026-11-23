package middleware

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type RequestLogger struct {
	logger *slog.Logger
}

func NewRequestLogger(logger *slog.Logger) *RequestLogger {
	return &RequestLogger{logger: logger}
}

func (l *RequestLogger) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &statusWriter{ResponseWriter: w}
		started := time.Now()
		defer func() {
			recovered := recover()
			status := response.status
			if status == 0 {
				status = http.StatusOK
				if recovered != nil {
					status = http.StatusInternalServerError
				}
			}
			args := []any{"method", r.Method, "path", r.URL.Path, "status", status, "duration", time.Since(started)}
			if recovered != nil {
				l.logger.ErrorContext(r.Context(), "http request panicked", args...)
				panic(recovered)
			}
			l.logger.InfoContext(r.Context(), "http request completed", args...)
		}()
		next.ServeHTTP(response, r)
	})
}

type statusWriter struct {
	http.ResponseWriter

	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if status >= http.StatusContinue && status < http.StatusOK && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	connection, rw, err := hijacker.Hijack()
	if err == nil && w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return connection, rw, err
}

func (w *statusWriter) Push(target string, options *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
