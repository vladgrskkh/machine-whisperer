package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

const (
	maxBatchSize = 1000
	maxBodyBytes = 16 << 20
	eventIDField = "event_id"
)

// time.Parse also accepts single-digit hours, comma fractions and overflowing offsets.
var timestampPattern = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`,
)

var errInvalidBatch = errors.New("invalid telemetry batch")

type BatchIngester interface {
	Execute(ctx context.Context, events []domain.Event) error
}

func NewBatch(ingest BatchIngester) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader := http.MaxBytesReader(w, r.Body, maxBodyBytes)
		events, err := decodeBatch(reader)
		_, oversized := errors.AsType[*http.MaxBytesError](err)
		switch {
		case oversized:
			http.Error(w, "batch too large", http.StatusRequestEntityTooLarge)
		case err != nil:
			http.Error(w, "invalid telemetry batch", http.StatusBadRequest)
		case ingest.Execute(r.Context(), events) != nil:
			http.Error(w, "telemetry storage unavailable", http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func decodeBatch(reader io.Reader) ([]domain.Event, error) {
	dec := json.NewDecoder(reader)
	for _, want := range []json.Token{json.Delim('{'), "events", json.Delim('[')} {
		if err := expect(dec, want); err != nil {
			return nil, err
		}
	}
	var events []domain.Event
	for dec.More() {
		if len(events) == maxBatchSize {
			return nil, errInvalidBatch
		}
		event, err := decodeEvent(dec)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil, errInvalidBatch
	}
	for _, want := range []json.Token{json.Delim(']'), json.Delim('}')} {
		if err := expect(dec, want); err != nil {
			return nil, err
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errInvalidBatch
	}
	return events, nil
}

func decodeEvent(dec *json.Decoder) (domain.Event, error) {
	var event domain.Event
	var raw json.RawMessage
	var receivedAt, observedAt string
	err := object(dec, map[string]any{
		eventIDField:  &event.EventID,
		"device_id":   &event.DeviceID,
		"machine_id":  &event.MachineID,
		"received_at": &receivedAt,
		"payload":     &raw,
	})
	if err != nil {
		return domain.Event{}, err
	}
	var vibration *float64
	p := &event.Payload
	err = object(json.NewDecoder(bytes.NewReader(raw)), map[string]any{
		"schema_version":        &p.SchemaVersion,
		eventIDField:            &p.EventID,
		"observed_at":           &observedAt,
		"operating_state":       &p.OperatingState,
		"vibration_rms_g":       &vibration,
		"dominant_frequency_hz": &p.DominantFrequencyHz,
		"current_rms_a":         &p.CurrentRMSA,
	})
	if err != nil || vibration == nil {
		return domain.Event{}, errInvalidBatch
	}
	event.ReceivedAt, err = parseTimestamp(receivedAt)
	if err != nil {
		return domain.Event{}, err
	}
	p.ObservedAt, err = parseTimestamp(observedAt)
	if err != nil {
		return domain.Event{}, err
	}
	p.VibrationRMSG = *vibration
	if !event.Valid() {
		return domain.Event{}, errInvalidBatch
	}
	event.ReceivedAt = event.ReceivedAt.UTC().Truncate(time.Microsecond)
	p.ObservedAt = p.ObservedAt.UTC().Truncate(time.Microsecond)
	return event, nil
}

func parseTimestamp(value string) (time.Time, error) {
	if !timestampPattern.MatchString(value) {
		return time.Time{}, errInvalidBatch
	}
	return time.Parse(time.RFC3339, value)
}

// Removing decoded fields rejects duplicate and incorrectly cased keys at each nesting level.
func object(dec *json.Decoder, fields map[string]any) error {
	if err := expect(dec, json.Delim('{')); err != nil {
		return err
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		dest, known := fields[name]
		if !ok || !known {
			return errInvalidBatch
		}
		if err = dec.Decode(dest); err != nil {
			return err
		}
		delete(fields, name)
	}
	return expect(dec, json.Delim('}'))
}

func expect(dec *json.Decoder, want json.Token) error {
	got, err := dec.Token()
	if err != nil {
		return err
	}
	if got != want {
		return errInvalidBatch
	}
	return nil
}
