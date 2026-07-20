package alerts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type NTFYConfig struct {
	URL   string
	Topic string
	Token string
}

type NTFYSender struct {
	endpoint string
	token    string
	client   *http.Client
}

func NewNTFYSender(config NTFYConfig, client *http.Client) (*NTFYSender, error) {
	base, err := url.Parse(strings.TrimSpace(config.URL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("alerts: ntfy URL must be an absolute http(s) URL")
	}
	if base.User != nil {
		return nil, errors.New("alerts: ntfy URL must not contain credentials")
	}
	topic := strings.TrimSpace(config.Topic)
	if !validTopic(topic) {
		return nil, errors.New("alerts: invalid ntfy topic")
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("alerts: ntfy URL must not contain query or fragment")
	}
	base.Path = path.Join(base.Path, topic)
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &NTFYSender{endpoint: base.String(), token: strings.TrimSpace(config.Token), client: client}, nil
}

func (s *NTFYSender) Send(ctx context.Context, delivery Delivery) error {
	if !validSingleLineText(delivery.Title, 512) {
		return errors.New("alerts: ntfy title must be single-line UTF-8 and at most 512 bytes")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(delivery.Message))
	if err != nil {
		return fmt.Errorf("alerts: create ntfy request: %w", err)
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	request.Header.Set("Title", delivery.Title)
	request.Header.Set("Priority", ntfyPriority(delivery.Severity))
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("alerts: send ntfy request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("alerts: ntfy returned %d: %s", response.StatusCode, message)
}

func ntfyPriority(severity Severity) string {
	switch severity {
	case SeverityCritical:
		return "urgent"
	case SeverityWarning:
		return "high"
	default:
		return "default"
	}
}

func validTopic(topic string) bool {
	if topic == "" || len(topic) > 64 {
		return false
	}
	for _, char := range topic {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}
