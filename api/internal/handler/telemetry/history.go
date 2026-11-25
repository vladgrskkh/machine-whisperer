package telemetry

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

const (
	defaultHistoryLimit = 100
	maxHistoryLimit     = 1000
	maxHistoryCursor    = 512
)

var historyTimestampPattern = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`,
)

type HistoryLister interface {
	Execute(ctx context.Context, ownerID, machineID string, limit int, cursor string) (telemetry.HistoryPage, error)
}

// NewHistory handles owner requests for telemetry history.
//
// @Summary Read machine telemetry history
// @ID listMachineTelemetry
// @Tags telemetry
// @Description The machine must belong to the caller. Observations are available after export.
// @Description Orders by descending observation time, device ID and event ID. Empty pages have an empty items array. An exhausted page has a null next_cursor.
// @Description Provide each of limit and cursor at most once.
// @Produce json
// @Security UserAccessJWT
// @Param machine_id path string true "Machine UUID." Format(uuid)
// @Param limit query int false "Maximum number of observations." minimum(1) maximum(1000) default(100)
// @Param cursor query string false "Opaque cursor bound to this machine. Use the previous next_cursor. Maximum 512 bytes." minlength(1) maxlength(512)
// @Success 200 {object} HistoryPage "Telemetry history page."
// @Failure 400 {object} ErrorResponse "Invalid machine ID, limit or cursor (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "The machine is unavailable to the caller (`machine_not_found`)."
// @Failure 503 {object} ErrorResponse "History could not be served. Retry later (`telemetry_unavailable`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/machines/{machine_id}/telemetry [get]
func NewHistory(list HistoryLister) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID, ok := middleware.UserID(r.Context())
		if !ok {
			middleware.Unauthorized(w)
			return
		}
		machineID, err := handler.ParseUUID(chi.URLParam(r, "machine_id"))
		if err != nil {
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid machine ID")
			return
		}
		limit, cursor, err := historyQuery(r.URL.RawQuery, machineID)
		if err == nil {
			var page telemetry.HistoryPage
			page, err = list.Execute(r.Context(), ownerID, machineID, limit, cursor)
			if err == nil {
				handler.JSON(w, http.StatusOK, page)
				return
			}
		}
		switch {
		case errors.Is(err, telemetry.ErrInvalidHistoryQuery):
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid telemetry history query")
		case errors.Is(err, machine.ErrNotFound):
			handler.Error(w, http.StatusNotFound, "machine_not_found", "Machine not found")
		case errors.Is(err, telemetry.ErrHistoryUnavailable):
			handler.Error(w, http.StatusServiceUnavailable, "telemetry_unavailable", "Telemetry history is unavailable")
		default:
			handler.InternalError(w)
		}
	})
}

func historyQuery(rawQuery, machineID string) (int, string, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values["limit"]) > 1 || len(values["cursor"]) > 1 {
		return 0, "", telemetry.ErrInvalidHistoryQuery
	}
	limit := defaultHistoryLimit
	if values.Has("limit") {
		limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > maxHistoryLimit {
			return 0, "", telemetry.ErrInvalidHistoryQuery
		}
	}
	cursor := values.Get("cursor")
	if values.Has("cursor") && !validHistoryCursor(cursor, machineID) {
		return 0, "", telemetry.ErrInvalidHistoryQuery
	}
	return limit, cursor, nil
}

func validHistoryCursor(cursor, machineID string) bool {
	if len(cursor) > maxHistoryCursor || strings.ContainsAny(cursor, "\r\n") {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return false
	}
	var boundMachine, deviceID, eventID, observedAt string
	if err = handler.DecodeObject(bytes.NewReader(raw), maxHistoryCursor, map[string]any{
		"machine_id":  &boundMachine,
		"observed_at": &observedAt,
		"device_id":   &deviceID,
		"event_id":    &eventID,
	}); err != nil || boundMachine != machineID {
		return false
	}
	device, deviceErr := handler.ParseUUID(deviceID)
	event, eventErr := handler.ParseUUID(eventID)
	if deviceErr != nil || device != deviceID || eventErr != nil || event != eventID ||
		!historyTimestampPattern.MatchString(observedAt) {
		return false
	}
	stamp, err := time.Parse(time.RFC3339, observedAt)
	// Match the range Analytics can bind to its DateTime64 column.
	const firstHistoryYear = 1900
	return err == nil && stamp.UTC().Year() >= firstHistoryYear && !stamp.After(time.Unix(0, math.MaxInt64))
}
