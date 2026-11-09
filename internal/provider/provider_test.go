package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAICompatible(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing bearer auth")
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "unit-test" || payload.Messages[0].Content != "hello" {
			t.Errorf("bad payload %+v", payload)
		}
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"response"}}]}`))
	}))
	defer upstream.Close()
	p := OpenAICompatible{URL: upstream.URL + "/v1", Model: "unit-test", APIKey: "secret"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	_, err := p.Generate(context.Background(), "hello")
	if !IsRetryable(err) {
		t.Fatalf("429 must retry, got %v", err)
	}
	response, err := p.Generate(context.Background(), "hello")
	if err != nil || response != "response" {
		t.Fatalf("got %q, %v", response, err)
	}
}

func TestProviderRejectsInsecureRemoteURL(t *testing.T) {
	p := OpenAICompatible{URL: "http://external.example/v1", Model: "x", APIKey: "x"}
	if err := p.Validate(); err == nil {
		t.Fatal("should reject insecure nonlocal HTTP")
	}
	if err := (OpenAICompatible{URL: "https://example.com/v1", Model: "x", APIKey: "x"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains((&ProcessError{Message: "upstream failed"}).Error(), "secret") {
		t.Fatal("leaked secret")
	}
}
