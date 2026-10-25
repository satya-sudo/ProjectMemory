package config

import "testing"

func TestLocalOnlyConfiguration(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "example.com:8080", "127.0.0.1:0", "127.0.0.1:65536"} {
		_, err := load(func(k string) string {
			if k == "PROJECTMAP_ADDR" {
				return addr
			}
			return "postgres://localhost/db"
		})
		if err == nil {
			t.Errorf("accepted unsafe/invalid address %q", addr)
		}
	}
	for _, addr := range []string{"", "127.0.0.1:8080", "[::1]:8080"} {
		_, err := load(func(k string) string {
			if k == "PROJECTMAP_ADDR" {
				return addr
			}
			return "postgres://localhost/db"
		})
		if err != nil {
			t.Errorf("rejected %q: %v", addr, err)
		}
	}
	if _, err := load(func(string) string { return "" }); err == nil {
		t.Fatal("accepted missing database URL")
	}
}
