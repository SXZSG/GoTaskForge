package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Provider interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

type ProcessError struct {
	Message   string
	Retryable bool
}

func (e *ProcessError) Error() string { return e.Message }

func IsRetryable(err error) bool {
	var p *ProcessError
	if errors.As(err, &p) {
		return p.Retryable
	}
	return errors.Is(err, context.DeadlineExceeded)
}

type Mock struct{ Delay time.Duration }

func (m Mock) Generate(ctx context.Context, prompt string) (string, error) {
	select {
	case <-time.After(m.Delay):
		return "[mock] 已处理请求：" + prompt, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type OpenAICompatible struct {
	URL    string // Base URL, e.g. https://api.openai.com/v1
	Model  string
	APIKey string
	Client *http.Client
}

func (p OpenAICompatible) Validate() error {
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" {
		return errors.New("invalid OPENAI_BASE_URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return errors.New("OPENAI_BASE_URL must use HTTPS (or HTTP for localhost)")
	}
	if p.APIKey == "" || p.Model == "" {
		return errors.New("OPENAI_API_KEY and OPENAI_MODEL are required")
	}
	return nil
}

func (p OpenAICompatible) Generate(ctx context.Context, prompt string) (string, error) {
	body := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{Model: p.Model}
	body.Messages = append(body.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: prompt})
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(p.URL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return "", &ProcessError{Message: "upstream request failed", Retryable: true}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Don't store or expose provider error bodies: they might contain sensitive data.
		retry := res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500
		return "", &ProcessError{Message: fmt.Sprintf("upstream returned HTTP %d", res.StatusCode), Retryable: retry}
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&parsed); err != nil {
		return "", &ProcessError{Message: "invalid upstream JSON", Retryable: true}
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return "", &ProcessError{Message: "upstream returned empty response", Retryable: true}
	}
	return parsed.Choices[0].Message.Content, nil
}
