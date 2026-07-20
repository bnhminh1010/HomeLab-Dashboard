package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type EnrollmentOptions struct {
	ServerURL   string
	Token       string
	Hostname    string
	DisplayName string
	HTTPClient  *http.Client
}

func Enroll(ctx context.Context, options EnrollmentOptions) (Credentials, error) {
	server, err := validateServerURL(options.ServerURL)
	if err != nil {
		return Credentials{}, err
	}
	if strings.TrimSpace(options.Token) == "" {
		return Credentials{}, errors.New("node agent: enrollment code is required")
	}
	if len(strings.TrimSpace(options.Token)) > 512 {
		return Credentials{}, errors.New("node agent: enrollment code is too large")
	}
	if strings.TrimSpace(options.Hostname) == "" || len(strings.TrimSpace(options.Hostname)) > 255 {
		return Credentials{}, errors.New("node agent: hostname is required")
	}
	if len(strings.TrimSpace(options.DisplayName)) > 80 {
		return Credentials{}, errors.New("node agent: display name is too large")
	}
	requestBody, err := json.Marshal(struct {
		Token       string `json:"token"`
		DisplayName string `json:"displayName,omitempty"`
		Hostname    string `json:"hostname"`
	}{Token: strings.TrimSpace(options.Token), DisplayName: strings.TrimSpace(options.DisplayName), Hostname: strings.TrimSpace(options.Hostname)})
	if err != nil {
		return Credentials{}, fmt.Errorf("node agent: encode enrollment request: %w", err)
	}
	endpoint := *server
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/agents/enroll"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(requestBody))
	if err != nil {
		return Credentials{}, fmt.Errorf("node agent: create enrollment request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return Credentials{}, fmt.Errorf("node agent: enrollment request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return Credentials{}, fmt.Errorf("node agent: enrollment rejected with HTTP %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, credentialFileLimit+1))
	if err != nil {
		return Credentials{}, fmt.Errorf("node agent: read enrollment response: %w", err)
	}
	if len(contents) > credentialFileLimit {
		return Credentials{}, errors.New("node agent: enrollment response exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	var result struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
		Credential      string `json:"credential"`
		ProtocolVersion int    `json:"protocolVersion"`
		WebsocketURL    string `json:"websocketUrl"`
	}
	if err := decoder.Decode(&result); err != nil {
		return Credentials{}, fmt.Errorf("node agent: decode enrollment response: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Credentials{}, errors.New("node agent: enrollment response must contain one JSON value")
	}
	credentials := Credentials{
		ServerURL: server.String(), WebsocketURL: result.WebsocketURL,
		NodeID: result.Node.ID, Credential: result.Credential, ProtocolVersion: result.ProtocolVersion,
	}
	if err := credentials.Validate(); err != nil {
		return Credentials{}, err
	}
	websocketURL, _ := resolveWebsocketURL(server, credentials.WebsocketURL)
	credentials.WebsocketURL = websocketURL.String()
	return credentials, nil
}
