package clickhouse

import (
	"context"
	"errors"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
)

const connectTimeout = 10 * time.Second

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
