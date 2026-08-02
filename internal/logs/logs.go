// Package logs provides a bounded read-only interface to the optional
// centralized log backend. It deliberately keeps Loki query syntax out of the
// browser-facing API.
package logs

import (
	"context"
	"errors"
	"time"
)

var (
	ErrDisabled = errors.New("logs: backend is disabled")
	ErrInvalid  = errors.New("logs: invalid query")
)

const (
	BackendDisabled = "disabled"
	BackendLoki     = "loki"
	LocalNodeID     = "local"
	MaxRange        = 7 * 24 * time.Hour
	DefaultLimit    = 200
	MaxLimit        = 500
)

type Status struct {
	Enabled        bool   `json:"enabled"`
	Backend        string `json:"backend"`
	NodeID         string `json:"nodeId"`
	RetentionHours int    `json:"retentionHours"`
}

type Query struct {
	NodeID    string
	From      time.Time
	To        time.Time
	Service   string
	Container string
	Level     string
	Text      string
	Limit     int
}

type Entry struct {
	Timestamp time.Time         `json:"timestamp"`
	Line      string            `json:"line"`
	Labels    map[string]string `json:"labels"`
}

type Result struct {
	Entries []Entry `json:"entries"`
}

type Reader interface {
	Status() Status
	Query(context.Context, Query) (Result, error)
}
