package config

import (
	"errors"
	"net"
	"os"
	"strconv"
)

type Config struct{ Addr, DatabaseURL string }

func Load() (Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (Config, error) {
	c := Config{Addr: getenv("PROJECTMAP_ADDR"), DatabaseURL: getenv("PROJECTMAP_DATABASE_URL")}
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
