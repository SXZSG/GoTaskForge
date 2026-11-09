package config

import "testing"

func TestDefaultAndValidation(t *testing.T) {
	t.Setenv("STORAGE_BACKEND", "file")
	t.Setenv("ADDR", "127.0.0.1:8080")
	t.Setenv("API_KEY", "")
	t.Setenv("PROVIDER", "mock")
	t.Setenv("REDIS_URL", "")
	t.Setenv("ALLOW_INSECURE_DEV", "false")
	c, err := Load()
	if err != nil || c.Workers <= 0 {
		t.Fatalf("config: %+v %v", c, err)
	}
	t.Setenv("ADDR", "0.0.0.0:8080")
	if _, err := Load(); err == nil {
		t.Fatal("expected API key check")
	}
	t.Setenv("API_KEY", "unit-secret")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}
