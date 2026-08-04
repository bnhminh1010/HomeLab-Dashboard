package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const queryTimeout = 5 * time.Second

var selectorValue = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)

type Loki struct {
	endpoint *url.URL
	client   *http.Client
}

func NewLoki(rawURL string, client *http.Client) (*Loki, error) {
	endpoint, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("logs: invalid Loki URL")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("logs: Loki URL must use HTTP or HTTPS")
	}
	if client == nil {
		client = &http.Client{Timeout: queryTimeout}
	}
	return &Loki{endpoint: endpoint, client: client}, nil
}

func (l *Loki) Status() Status {
	return Status{Enabled: true, Backend: BackendLoki, NodeID: LocalNodeID, RetentionHours: int(MaxRange / time.Hour)}
}

func (l *Loki) Query(ctx context.Context, query Query) (Result, error) {
	if err := validateQuery(&query); err != nil {
		return Result{}, err
	}
	requestURL := *l.endpoint
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + "/loki/api/v1/query_range"
	values := requestURL.Query()
	values.Set("query", logQL(query))
	values.Set("start", strconv.FormatInt(query.From.UnixNano(), 10))
	values.Set("end", strconv.FormatInt(query.To.UnixNano(), 10))
	values.Set("limit", strconv.Itoa(query.Limit))
	values.Set("direction", "backward")
	requestURL.RawQuery = values.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return Result{}, fmt.Errorf("logs: create Loki request: %w", err)
	}
	response, err := l.client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("logs: query Loki: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("logs: Loki returned HTTP %d", response.StatusCode)
	}
	var payload lokiResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return Result{}, fmt.Errorf("logs: decode Loki response: %w", err)
	}
	entries := make([]Entry, 0)
	for _, stream := range payload.Data.Result {
		for _, value := range stream.Values {
			if len(value) != 2 {
				continue
			}
			nanoseconds, err := strconv.ParseInt(value[0], 10, 64)
			if err != nil {
				continue
			}
			entries = append(entries, Entry{Timestamp: time.Unix(0, nanoseconds).UTC(), Line: value[1], Labels: stream.Stream})
		}
	}
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].Timestamp.After(entries[right].Timestamp) })
	if len(entries) > query.Limit {
		entries = entries[:query.Limit]
	}
	return Result{Entries: entries}, nil
}

type lokiResponse struct {
	Data struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func validateQuery(query *Query) error {
	query.NodeID = strings.TrimSpace(query.NodeID)
	if query.NodeID == "" {
		query.NodeID = LocalNodeID
	}
	if query.NodeID != LocalNodeID || query.From.IsZero() || query.To.IsZero() || !query.To.After(query.From) || query.To.Sub(query.From) > MaxRange {
		return ErrInvalid
	}
	for _, value := range []string{query.Service, query.Container} {
		if value != "" && !selectorValue.MatchString(value) {
			return ErrInvalid
		}
	}
	query.Level = strings.ToLower(strings.TrimSpace(query.Level))
	if query.Level != "" && query.Level != "debug" && query.Level != "info" && query.Level != "warn" && query.Level != "error" {
		return ErrInvalid
	}
	query.Text = strings.TrimSpace(query.Text)
	if len(query.Text) > 160 || strings.ContainsAny(query.Text, "\x00\r\n") {
		return ErrInvalid
	}
	if query.IsRegex && query.Text != "" {
		if _, err := regexp.Compile(query.Text); err != nil {
			return ErrInvalid
		}
	}
	if query.Limit == 0 {
		query.Limit = DefaultLimit
	}
	if query.Limit < 1 || query.Limit > MaxLimit {
		return ErrInvalid
	}
	return nil
}

func logQL(query Query) string {
	labels := []string{`job="podman"`, `node="local"`}
	if query.Service != "" {
		labels = append(labels, `service_name="`+query.Service+`"`)
	}
	expression := "{" + strings.Join(labels, ",") + "}"
	if query.Container != "" || query.Level != "" {
		expression += " | json"
	}
	if query.Container != "" {
		expression += ` | container_name="` + query.Container + `"`
	}
	if query.Level != "" {
		expression += ` | level=~"(?i)^` + query.Level + `$"`
	}
	if query.Text != "" {
		operator := "|="
		if query.IsRegex {
			operator = "|~"
		}
		expression += " " + operator + " " + strconv.Quote(query.Text)
	}
	return expression
}
