package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr, Backend, DataFile, MySQLDSN, RedisURL                                          string
	Provider, OpenAIBaseURL, OpenAIModel, OpenAIAPIKey, APIKey                           string
	Workers, MaxPending, MaxAttempts, TaskTimeoutSeconds, RatePerMinute                  int
	GatewayMaxPerBackend, GatewayTimeoutSec, GatewayFailureThreshold, GatewayCooldownSec int
	GatewayBackends                                                                      []string
	GatewayKey                                                                           string
	GatewayAllowHTTP                                                                     bool
	AllowInsecureDev                                                                     bool
}

func getenv(k, defaultValue string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return defaultValue
}
func positiveInt(k string, fallback int) (int, error) {
	x, err := strconv.Atoi(getenv(k, strconv.Itoa(fallback)))
	if err != nil || x < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", k)
	}
	return x, nil
}
func Load() (Config, error) {
	c := Config{
		Addr: getenv("ADDR", "127.0.0.1:8080"), Backend: getenv("STORAGE_BACKEND", "file"),
		DataFile: getenv("DATA_FILE", "./data/jobs.json"), MySQLDSN: os.Getenv("MYSQL_DSN"),
		RedisURL: os.Getenv("REDIS_URL"), Provider: getenv("PROVIDER", "mock"),
		OpenAIBaseURL: getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		OpenAIModel:   os.Getenv("OPENAI_MODEL"), OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
		APIKey: os.Getenv("API_KEY"), AllowInsecureDev: os.Getenv("ALLOW_INSECURE_DEV") == "true",
		GatewayKey: os.Getenv("GATEWAY_UPSTREAM_KEY"), GatewayAllowHTTP: os.Getenv("GATEWAY_ALLOW_HTTP") == "true",
	}
	var err error
	for _, entry := range []struct {
		key      string
		fallback int
		field    *int
	}{
		{"WORKERS", 4, &c.Workers}, {"MAX_PENDING", 100, &c.MaxPending},
		{"MAX_ATTEMPTS", 3, &c.MaxAttempts}, {"TASK_TIMEOUT_SEC", 30, &c.TaskTimeoutSeconds},
		{"RATE_PER_MINUTE", 60, &c.RatePerMinute},
		{"GATEWAY_MAX_PER_BACKEND", 4, &c.GatewayMaxPerBackend},
		{"GATEWAY_TIMEOUT_SEC", 90, &c.GatewayTimeoutSec},
		{"GATEWAY_FAILURE_THRESHOLD", 3, &c.GatewayFailureThreshold},
		{"GATEWAY_COOLDOWN_SEC", 15, &c.GatewayCooldownSec},
	} {
		*entry.field, err = positiveInt(entry.key, entry.fallback)
		if err != nil {
			return Config{}, err
		}
	}
	if raw := strings.TrimSpace(os.Getenv("GATEWAY_BACKENDS")); raw != "" {
		for _, b := range strings.Split(raw, ",") {
			c.GatewayBackends = append(c.GatewayBackends, strings.TrimSpace(b))
		}
	}
	if c.GatewayTimeoutSec > 600 || c.GatewayMaxPerBackend > 10000 || c.GatewayCooldownSec > 3600 {
		return c, errors.New("invalid gateway upper bound")
	}
	if c.Backend != "file" && c.Backend != "mysql" {
		return c, errors.New("STORAGE_BACKEND must be file or mysql")
	}
	if c.Backend == "mysql" && strings.TrimSpace(c.MySQLDSN) == "" {
		return c, errors.New("MYSQL_DSN is required with mysql backend")
	}
	if c.Provider != "mock" && c.Provider != "openai" {
		return c, errors.New("PROVIDER must be mock or openai")
	}
	if c.Workers > c.MaxPending {
		return c, errors.New("WORKERS cannot exceed MAX_PENDING")
	}
	if c.TaskTimeoutSeconds > 3600 {
		return c, errors.New("TASK_TIMEOUT_SEC cannot exceed 3600")
	}
	// Docker exposes 0.0.0.0. A shared secret is mandatory by default.
	if (strings.HasPrefix(c.Addr, "0.0.0.0:") || strings.HasPrefix(c.Addr, ":")) &&
		c.APIKey == "" && !c.AllowInsecureDev {
		return c, errors.New("API_KEY required on non-loopback bind; set ALLOW_INSECURE_DEV=true only for isolated local experiments")
	}
	if c.RedisURL != "" && c.APIKey == "" && !c.AllowInsecureDev {
		return c, errors.New("API_KEY required when REDIS_URL is configured")
	}
	return c, nil
}

func (c Config) TaskTimeout() time.Duration { return time.Duration(c.TaskTimeoutSeconds) * time.Second }
