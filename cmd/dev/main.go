// Dev runs project checks with the same dotenv policy as the backend.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"projectmap/internal/config"
)

func main() {
	if err := run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: dev test|test-db|verify-local")
	}
	var command []string
	switch os.Args[1] {
	case "test":
		command = []string{"go", "test", "-race", "./..."}
	case "test-db":
		command = []string{"go", "test", "-race", "-count=1", "-v", "./internal/database"}
	case "verify-local":
		command = []string{"python3", "scripts/verify_local.py"}
	default:
		return errors.New("unknown development command")
	}
	if err := config.LoadEnv(".env"); err != nil {
		return err
	}
	if os.Args[1] == "test-db" {
		if _, exists := os.LookupEnv("PROJECTMAP_TEST_DATABASE_URL"); !exists {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := os.Setenv("PROJECTMAP_TEST_DATABASE_URL", cfg.DatabaseURL); err != nil {
				return errors.New("cannot configure database test environment")
			}
		}
		if os.Getenv("PROJECTMAP_TEST_DATABASE_URL") == "" {
			return errors.New("PROJECTMAP_TEST_DATABASE_URL must not be empty")
		}
	}
	child := exec.Command(command[0], command[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	return child.Run()
}
