package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

const (
	maxBatchSize = 1000
	maxBodyBytes = 16 << 20
)

var errInvalidBatch = errors.New("invalid telemetry batch")

type BatchWriter interface {
	WriteBatch(ctx context.Context, events []domain.Event) error
}

func New(key string, writer BatchWriter) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/telemetry-batches", requireKey(key, func(w http.ResponseWriter, r *http.Request) {
		reader := http.MaxBytesReader(w, r.Body, maxBodyBytes)
		events, err := decodeBatch(reader)
		_, oversized := errors.AsType[*http.MaxBytesError](err)
		switch {
		case oversized:
			http.Error(w, "batch too large", http.StatusRequestEntityTooLarge)
		case err != nil:
			http.Error(w, "invalid telemetry batch", http.StatusBadRequest)
		case writer.WriteBatch(r.Context(), events) != nil:
			http.Error(w, "telemetry storage unavailable", http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	return mux
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
	err := object(dec, map[string]any{
		"event_id":    &event.EventID,
		"device_id":   &event.DeviceID,
		"machine_id":  &event.MachineID,
		"received_at": &event.ReceivedAt,
		"payload":     &raw,
	})
	if err != nil {
		return domain.Event{}, err
	}
	var vibration *float64
	p := &event.Payload
	err = object(json.NewDecoder(bytes.NewReader(raw)), map[string]any{
		"schema_version":        &p.SchemaVersion,
		"event_id":              &p.EventID,
		"observed_at":           &p.ObservedAt,
		"operating_state":       &p.OperatingState,
		"vibration_rms_g":       &vibration,
		"dominant_frequency_hz": &p.DominantFrequencyHz,
		"current_rms_a":         &p.CurrentRMSA,
	})
	if err != nil || vibration == nil {
		return domain.Event{}, errInvalidBatch
	}
	p.VibrationRMSG = *vibration
	if !event.Valid() {
		return domain.Event{}, errInvalidBatch
	}
	return event, nil
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
