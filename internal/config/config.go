package config

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	loadOnce sync.Once
	values   map[string]string
)

func load() {
	values = map[string]string{}
	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		path = "config.yaml"
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return
	}
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return
	}
	for k, v := range raw {
		values[strings.ToUpper(k)] = toString(v)
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

// Get returns the configuration value for key. Precedence:
// 1) Environment variable
// 2) config file value (config.yaml)
// 3) provided default
func Get(key, def string) string {
	loadOnce.Do(load)
	if v := os.Getenv(key); v != "" {
		return v
	}
	// Try exact, upper, and lower keys
	if values != nil {
		if v, ok := values[key]; ok && v != "" {
			return v
		}
		if v, ok := values[strings.ToUpper(key)]; ok && v != "" {
			return v
		}
		if v, ok := values[strings.ToLower(key)]; ok && v != "" {
			return v
		}
	}
	return def
}
