package alerts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WebhookConfig configures a generic alert webhook. The secret is used to
// sign the exact request body with HMAC-SHA256; receivers can verify the
// X-Homelab-Signature header before decoding the JSON payload.
type WebhookConfig struct {
	URL    string
	Secret string
}

// WebhookSender delivers alert lifecycle events to an operator-owned HTTP
// endpoint. It intentionally supports only HTTP(S), with no credentials in
// the URL, so authentication stays in the HMAC secret rather than logs.
type WebhookSender struct {
	endpoint string
	secret   []byte
	client   *http.Client
}

type webhookPayload struct {
	Version   string       `json:"version"`
	Event     DeliveryKind `json:"event"`
	Severity  Severity     `json:"severity"`
	Title     string       `json:"title"`
	Message   string       `json:"message"`
	AlertKey  AlertKey     `json:"alertKey"`
	Delivery  int64        `json:"deliveryId"`
	Attempt   int          `json:"attempt"`
	CreatedAt time.Time    `json:"createdAt"`
}

func NewWebhookSender(config WebhookConfig, client *http.Client) (*WebhookSender, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.URL))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, errors.New("alerts: webhook URL must be an absolute http(s) URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("alerts: webhook URL must not contain credentials, query, or fragment")
	}
	secret := strings.TrimSpace(config.Secret)
	if len(secret) < 16 || len(secret) > 4096 {
		return nil, errors.New("alerts: webhook secret must be between 16 and 4096 bytes")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WebhookSender{endpoint: endpoint.String(), secret: []byte(secret), client: client}, nil
}

func (s *WebhookSender) Send(ctx context.Context, delivery Delivery) error {
	if !validSingleLineText(delivery.Title, 512) {
		return errors.New("alerts: webhook title must be single-line UTF-8 and at most 512 bytes")
	}
	if len(delivery.Message) > 64*1024 {
		return errors.New("alerts: webhook message exceeds 65536 bytes")
	}
	payload, err := json.Marshal(webhookPayload{
		Version: "homelab-dashboard.webhook/v1", Event: delivery.Kind,
		Severity: delivery.Severity, Title: delivery.Title, Message: delivery.Message,
		AlertKey: delivery.AlertKey, Delivery: delivery.ID, Attempt: delivery.Attempts,
		CreatedAt: delivery.CreatedAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("alerts: encode webhook payload: %w", err)
	}
	digest := hmac.New(sha256.New, s.secret)
	_, _ = digest.Write(payload)
	signature := "sha256=" + hex.EncodeToString(digest.Sum(nil))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("alerts: create webhook request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "homelab-dashboard-webhook/1")
	request.Header.Set("X-Homelab-Signature", signature)
	request.Header.Set("X-Homelab-Event", string(delivery.Kind))
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("alerts: send webhook request: %w", err)
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
	return fmt.Errorf("alerts: webhook returned %d: %s", response.StatusCode, message)
}
