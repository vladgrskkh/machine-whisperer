package clickhouse

import (
	"context"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

const insertTimeout = 25 * time.Second

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
