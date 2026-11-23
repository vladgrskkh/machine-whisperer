package clickhouse

import (
	"context"
	"errors"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

const (
	connectTimeout = 10 * time.Second
	insertTimeout  = 25 * time.Second
)

func Open(ctx context.Context, c config.ClickHouse) (ch.Conn, error) {
	conn, err := ch.Open(&ch.Options{
		Addr:        []string{c.Address},
		Auth:        ch.Auth{Database: c.Database, Username: c.User, Password: c.Password},
		DialTimeout: connectTimeout,
	})
	if err != nil {
		return nil, errors.New("invalid ClickHouse configuration")
	}
	startup, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err = conn.Ping(startup); err != nil {
		_ = conn.Close()
		return nil, errors.New("ClickHouse connection failed")
	}
	return conn, nil
}

type Writer struct {
	conn ch.Conn
}

func NewWriter(conn ch.Conn) *Writer {
	return &Writer{conn: conn}
}

func (w *Writer) WriteBatch(ctx context.Context, events []domain.Event) error {
	ctx, cancel := context.WithTimeout(ctx, insertTimeout)
	defer cancel()
	ctx = ch.Context(ctx, ch.WithSettings(ch.Settings{"async_insert": 0}))
	batch, err := w.conn.PrepareBatch(ctx, `INSERT INTO telemetry_events
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
