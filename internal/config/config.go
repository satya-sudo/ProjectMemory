package config

import (
	"errors"
	"net"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct{ Addr, DatabaseURL, CredentialsFile string }

func Load() (Config, error) {
	if err := LoadEnv(".env"); err != nil {
		return Config{}, err
	}
	return load(os.Getenv)
}

// LoadEnv fills missing process environment variables without overriding existing
// values, including explicitly empty ones. Never expose parser errors: they can
// contain credentials from the file.
func LoadEnv(path string) error {
	if err := godotenv.Load(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot read or parse .env file")
	}
	return nil
}

func load(getenv func(string) string) (Config, error) {
	c := Config{Addr: getenv("PROJECTMAP_ADDR"), DatabaseURL: getenv("PROJECTMAP_DATABASE_URL")}
	c.CredentialsFile = getenv("PROJECTMAP_CREDENTIALS_FILE")
	if c.CredentialsFile == "" {
		c.CredentialsFile = ".local/credentials.json"
	}
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return Config{}, errors.New("PROJECTMAP_ADDR must be a loopback IP and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return Config{}, errors.New("PROJECTMAP_ADDR must use a loopback IP")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return Config{}, errors.New("PROJECTMAP_ADDR has an invalid port")
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("PROJECTMAP_DATABASE_URL is required")
	}
	return c, nil
}
