package config

import (
	"bytes"
	"errors"
	"os"
	"strings"
)

type ClickHouse struct {
	Address  string
	Database string
	User     string
	Password string
}

type Config struct {
	ClickHouse ClickHouse
	Address    string
	IngestKey  string
}

func Load() (Config, error) {
	db, err := LoadClickHouse()
	if err != nil {
		return Config{}, err
	}
	key, err := loadSecret("ANALYTICS_INGEST_API_KEY")
	if err != nil {
		return Config{}, err
	}
	if key == "" || strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return Config{}, errors.New("ANALYTICS_INGEST_API_KEY must be a nonempty token")
	}
	addr := os.Getenv("ANALYTICS_ADDRESS")
	if addr == "" {
		addr = ":8081"
	}
	return Config{ClickHouse: db, Address: addr, IngestKey: key}, nil
}

const clickHousePasswordLength = 64

func LoadClickHouse() (ClickHouse, error) {
	password, err := loadSecret("CLICKHOUSE_PASSWORD")
	if err != nil {
		return ClickHouse{}, err
	}
	if os.Getenv("CLICKHOUSE_PASSWORD_FILE") != "" &&
		(len(password) != clickHousePasswordLength || strings.Trim(password, "0123456789abcdef") != "") {
		return ClickHouse{}, errors.New(
			"CLICKHOUSE_PASSWORD_FILE must contain exactly 64 lowercase hexadecimal characters",
		)
	}
	c := ClickHouse{
		Address:  os.Getenv("CLICKHOUSE_ADDRESS"),
		Database: os.Getenv("CLICKHOUSE_DATABASE"),
		User:     os.Getenv("CLICKHOUSE_USER"),
		Password: password,
	}
	if strings.TrimSpace(c.Address) == "" || strings.TrimSpace(c.Database) == "" || strings.TrimSpace(c.User) == "" ||
		c.Password == "" {
		return ClickHouse{}, errors.New("ClickHouse address, database, user and password are required")
	}
	return c, nil
}

func loadSecret(variable string) (string, error) {
	fileVariable := variable + "_FILE"
	path := os.Getenv(fileVariable)
	if path == "" {
		return os.Getenv(variable), nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // The deployment operator configures the secret path.
	if err != nil {
		return "", errors.New("cannot read " + fileVariable)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New(fileVariable + " must not contain NUL bytes")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
