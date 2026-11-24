package clickhouse

import (
	"context"
	"strconv"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

const (
	insertTimeout = 25 * time.Second
	queryTimeout  = 10 * time.Second
)

type Repository struct {
	conn ch.Conn
}

func NewRepository(conn ch.Conn) *Repository {
	return &Repository{conn: conn}
}

func (r *Repository) StoreBatch(ctx context.Context, events []domain.Event) error {
	ctx, cancel := context.WithTimeout(ctx, insertTimeout)
	defer cancel()
	ctx = ch.Context(ctx, ch.WithSettings(ch.Settings{"async_insert": 0}))
	batch, err := r.conn.PrepareBatch(ctx, `INSERT INTO telemetry_events
		(schema_version, event_id, device_id, machine_id, observed_at, received_at, operating_state,
		 vibration_rms_g, dominant_frequency_hz, current_rms_a)`)
	if err != nil {
		return err
	}
	defer batch.Close()
	for _, event := range events {
		p := event.Payload
		if err = batch.Append(
			p.SchemaVersion,
			event.EventID,
			event.DeviceID,
			event.MachineID,
			p.ObservedAt.UTC(),
			event.ReceivedAt.UTC(),
			p.OperatingState,
			p.VibrationRMSG,
			p.DominantFrequencyHz,
			p.CurrentRMSA,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

func (r *Repository) List(ctx context.Context, query domain.HistoryQuery) ([]domain.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	sql := `SELECT schema_version, event_id, device_id, observed_at, received_at,
		operating_state, vibration_rms_g, dominant_frequency_hz, current_rms_a
		FROM telemetry_events FINAL WHERE machine_id = {machine_id:UUID}`
	params := ch.Parameters{"machine_id": query.MachineID, "limit": strconv.Itoa(query.Limit)}
	if query.Cursor != nil {
		sql += ` AND (observed_at, device_id, event_id) <
			({observed_at:DateTime64(6, 'UTC')}, {device_id:UUID}, {event_id:UUID})`
		params["observed_at"] = query.Cursor.ObservedAt.UTC().Format("2006-01-02 15:04:05.000000")
		params["device_id"] = query.Cursor.DeviceID
		params["event_id"] = query.Cursor.EventID
	}
	sql += ` ORDER BY observed_at DESC, device_id DESC, event_id DESC LIMIT {limit:UInt64}`

	rows, err := r.conn.Query(ch.Context(ctx, ch.WithParameters(params)), sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.Event
	for rows.Next() {
		event := domain.Event{MachineID: query.MachineID}
		p := &event.Payload
		err = rows.Scan(
			&p.SchemaVersion, &event.EventID, &event.DeviceID, &p.ObservedAt, &event.ReceivedAt,
			&p.OperatingState, &p.VibrationRMSG, &p.DominantFrequencyHz, &p.CurrentRMSA,
		)
		if err != nil {
			return nil, err
		}
		p.EventID = event.EventID
		p.ObservedAt = p.ObservedAt.UTC()
		event.ReceivedAt = event.ReceivedAt.UTC()
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
