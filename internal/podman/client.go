package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIVersion = "v5.0.0"
	maxJSONBody       = 8 << 20
)

type Option func(*Client)

func WithAPIVersion(version string) Option {
	return func(c *Client) {
		c.apiVersion = strings.Trim(version, "/")
	}
}

type Client struct {
	socketPath string
	apiVersion string
	httpClient *http.Client
}

func NewClient(socketPath string, options ...Option) (*Client, error) {
	if strings.TrimSpace(socketPath) == "" {
		return nil, errors.New("podman: socket path is required")
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   16,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	c := &Client{
		socketPath: socketPath,
		apiVersion: defaultAPIVersion,
		httpClient: &http.Client{Transport: transport},
	}
	for _, option := range options {
		option(c)
	}
	if !validAPIVersion(c.apiVersion) {
		return nil, errors.New("podman: invalid API version")
	}
	return c, nil
}

func (c *Client) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
}

func (c *Client) ListContainers(ctx context.Context, all bool) ([]Container, error) {
	query := url.Values{"all": {strconv.FormatBool(all)}}
	var wire []containerWire
	if err := c.doJSON(ctx, http.MethodGet, "/containers/json", query, nil, &wire); err != nil {
		return nil, err
	}

	containers := make([]Container, 0, len(wire))
	for _, item := range wire {
		if IsHidden(item.Labels) {
			continue
		}
		name := item.Name
		if name == "" && len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		ports := make([]Port, 0, len(item.Ports))
		for _, port := range item.Ports {
			ports = append(ports, Port{
				HostIP:        port.HostIP,
				ContainerPort: port.ContainerPort,
				HostPort:      port.HostPort,
				Range:         port.Range,
				Protocol:      port.Protocol,
			})
		}
		created := int64(0)
		if !item.Created.IsZero() {
			created = item.Created.Unix()
		}
		containers = append(containers, Container{
			ID:        item.ID,
			Name:      name,
			Image:     item.Image,
			State:     item.State,
			Status:    item.Status,
			Created:   created,
			Labels:    item.Labels,
			Ports:     ports,
			Protected: IsProtected(item.Labels),
		})
	}
	return containers, nil
}

func (c *Client) InspectContainer(ctx context.Context, containerID string) (ContainerDetails, error) {
	if err := validateIdentifier(containerID); err != nil {
		return ContainerDetails{}, err
	}
	var wire inspectWire
	if err := c.doJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(containerID)+"/json", nil, nil, &wire); err != nil {
		return ContainerDetails{}, err
	}
	labels := wire.Config.Labels
	if labels == nil {
		labels = wire.Labels
	}
	return ContainerDetails{
		Container: Container{
			ID:        wire.ID,
			Name:      strings.TrimPrefix(wire.Name, "/"),
			Image:     wire.ImageName,
			State:     wire.State.Status,
			Labels:    labels,
			Protected: IsProtected(labels),
		},
		Running:      wire.State.Running,
		Health:       wire.State.Health.Status,
		RestartCount: wire.RestartCount,
		StartedAt:    wire.State.StartedAt,
	}, nil
}

func (c *Client) Stats(ctx context.Context, all bool) ([]ContainerStats, error) {
	query := url.Values{
		"all":    {strconv.FormatBool(all)},
		"stream": {"false"},
	}
	body, err := c.do(ctx, http.MethodGet, "/containers/stats", query, nil, "")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	payload, err := io.ReadAll(io.LimitReader(body, maxJSONBody+1))
	if err != nil {
		return nil, fmt.Errorf("podman: read stats response: %w", err)
	}
	if len(payload) > maxJSONBody {
		return nil, errors.New("podman: stats response exceeds 8 MiB")
	}

	var wire []statsWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		var envelope struct {
			Stats []statsWire `json:"Stats"`
		}
		if envelopeErr := json.Unmarshal(payload, &envelope); envelopeErr != nil {
			return nil, fmt.Errorf("podman: decode stats response: %w", err)
		}
		wire = envelope.Stats
	}
	stats := make([]ContainerStats, 0, len(wire))
	for _, item := range wire {
		var networkIn, networkOut uint64
		for _, network := range item.Network {
			networkIn += network.RXBytes
			networkOut += network.TXBytes
		}
		stats = append(stats, ContainerStats{
			ID:          item.ContainerID,
			Name:        item.Name,
			CPUPercent:  item.CPU,
			MemoryUsage: item.MemUsage,
			MemoryLimit: item.MemLimit,
			NetworkIn:   networkIn,
			NetworkOut:  networkOut,
			BlockInput:  item.BlockInput,
			BlockOutput: item.BlockOutput,
			PIDs:        item.PIDs,
			UpTime:      uint64(item.UpTime / time.Second),
		})
	}
	return stats, nil
}

func (c *Client) Logs(ctx context.Context, containerID string, options LogsOptions) (io.ReadCloser, error) {
	if err := validateIdentifier(containerID); err != nil {
		return nil, err
	}
	details, err := c.InspectContainer(ctx, containerID)
	if err != nil {
		return nil, err
	}
	if details.Protected || IsHidden(details.Labels) {
		return nil, ErrProtectedContainer
	}
	if options.Tail == 0 {
		options.Tail = 200
	}
	if options.Tail > 500 {
		return nil, errors.New("podman: log tail must not exceed 500 lines")
	}
	query := url.Values{
		"follow":     {strconv.FormatBool(options.Follow)},
		"stdout":     {"true"},
		"stderr":     {"true"},
		"timestamps": {"false"},
		"tail":       {strconv.FormatUint(uint64(options.Tail), 10)},
	}
	return c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(containerID)+"/logs", query, nil, "")
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, query url.Values, input, output any) error {
	var requestBody io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("podman: encode request: %w", err)
		}
		requestBody = bytes.NewReader(payload)
	}
	body, err := c.do(ctx, method, endpoint, query, requestBody, "application/json")
	if err != nil {
		return err
	}
	defer body.Close()
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(body, maxJSONBody))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(body, maxJSONBody))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("podman: decode response: %w", err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, endpoint string, query url.Values, body io.Reader, contentType string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.url(endpoint, query), body)
	if err != nil {
		return nil, fmt.Errorf("podman: create request: %w", err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("podman: %s %s: %w", method, endpoint, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		err := decodeAPIError(response)
		response.Body.Close()
		return nil, err
	}
	return response.Body, nil
}

func (c *Client) url(endpoint string, query url.Values) string {
	u := url.URL{
		Scheme:   "http",
		Host:     "podman",
		Path:     path.Join("/", c.apiVersion, "libpod", endpoint),
		RawQuery: query.Encode(),
	}
	return u.String()
}

func decodeAPIError(response *http.Response) error {
	var body struct {
		Message  string `json:"message"`
		Response string `json:"response"`
		Cause    string `json:"cause"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body)
	message := body.Message
	if message == "" {
		message = body.Response
	}
	if message == "" {
		message = body.Cause
	}
	return &APIError{StatusCode: response.StatusCode, Message: message}
}

func validateIdentifier(value string) error {
	if value == "" || len(value) > 128 {
		return errors.New("podman: invalid container or exec identifier")
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '.' || character == '-' {
			continue
		}
		return errors.New("podman: invalid container or exec identifier")
	}
	return nil
}

func validAPIVersion(value string) bool {
	if len(value) < 2 || value[0] != 'v' {
		return false
	}
	parts := strings.Split(value[1:], ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

type containerWire struct {
	ID      string            `json:"Id"`
	Name    string            `json:"Name"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created time.Time         `json:"Created"`
	Labels  map[string]string `json:"Labels"`
	Ports   []portWire        `json:"Ports"`
}

type portWire struct {
	HostIP        string `json:"host_ip"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	Range         uint16 `json:"range"`
	Protocol      string `json:"protocol"`
}

type inspectWire struct {
	ID           string            `json:"Id"`
	Name         string            `json:"Name"`
	ImageName    string            `json:"ImageName"`
	Labels       map[string]string `json:"Labels"`
	RestartCount uint64            `json:"RestartCount"`
	Config       struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string    `json:"Status"`
		Running   bool      `json:"Running"`
		StartedAt time.Time `json:"StartedAt"`
		Health    struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

type statsWire struct {
	ContainerID string                      `json:"ContainerID"`
	Name        string                      `json:"Name"`
	CPU         float64                     `json:"CPU"`
	MemUsage    uint64                      `json:"MemUsage"`
	MemLimit    uint64                      `json:"MemLimit"`
	Network     map[string]networkStatsWire `json:"Network"`
	BlockInput  uint64                      `json:"BlockInput"`
	BlockOutput uint64                      `json:"BlockOutput"`
	PIDs        uint64                      `json:"PIDs"`
	UpTime      time.Duration               `json:"UpTime"`
}

type networkStatsWire struct {
	RXBytes uint64 `json:"RxBytes"`
	TXBytes uint64 `json:"TxBytes"`
}
