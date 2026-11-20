// Package-level .env loading: the Deno dev tasks passed --env-file=./.env;
// the Go server applies the same dev convenience, with real environment
// variables always winning (the container image passes real env and ships
// no .env — this only affects local development).
package app

import (
	"os"
	"strconv"
	"strings"
)

// LoadDotEnv applies KEY=VALUE lines from path when the file exists.
// Existing environment variables are never overridden. Comments (#) and
// blank lines are ignored; values are trimmed; a line without "=" is
// skipped silently (Deno's --env-file is similarly lenient).
func LoadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if quoted, err := strconv.QuotedPrefix(value); err == nil && len(quoted) == len(value) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}
