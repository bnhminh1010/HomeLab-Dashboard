// Package topology defines manually curated service dependencies.
//
// An edge always points from the service that depends on another service to
// the dependency it needs. Cycles are valid because real deployments can
// intentionally expose mutually dependent services.
package topology

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidDependency   = errors.New("topology: invalid dependency")
	ErrSelfDependency      = errors.New("topology: a service cannot depend on itself")
	ErrDuplicateDependency = errors.New("topology: dependency already exists")
	ErrDependencyNotFound  = errors.New("topology: dependency not found")
	ErrServiceNotFound     = errors.New("topology: referenced service not found")
	ErrNodeNotFound        = errors.New("topology: node is not enrolled")
)

// Dependency is one manually configured, directed service relationship.
// DependentServiceID -> DependencyServiceID is the edge direction.
type Dependency struct {
	ID                  int64     `json:"id"`
	NodeID              string    `json:"nodeId"`
	DependentServiceID  string    `json:"dependentServiceId"`
	DependencyServiceID string    `json:"dependencyServiceId"`
	Label               string    `json:"label,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// DependencyInput contains the editable part of a topology edge.
type DependencyInput struct {
	NodeID              string `json:"nodeId"`
	DependentServiceID  string `json:"dependentServiceId"`
	DependencyServiceID string `json:"dependencyServiceId"`
	Label               string `json:"label,omitempty"`
}

// NormalizeInput trims user-facing values before validation and persistence.
func NormalizeInput(input DependencyInput) DependencyInput {
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.DependentServiceID = strings.TrimSpace(input.DependentServiceID)
	input.DependencyServiceID = strings.TrimSpace(input.DependencyServiceID)
	input.Label = strings.TrimSpace(input.Label)
	return input
}

// ValidateInput protects the topology store from malformed identifiers and
// preserves the semantic direction of an edge. It intentionally does not
// reject cycles: an operator may model one explicitly.
func ValidateInput(input DependencyInput) error {
	input = NormalizeInput(input)
	if err := ValidateNodeID(input.NodeID); err != nil {
		return err
	}
	if !validIdentifier(input.DependentServiceID, 128) {
		return fmt.Errorf("%w: dependent service id is required and must be a safe identifier", ErrInvalidDependency)
	}
	if !validIdentifier(input.DependencyServiceID, 128) {
		return fmt.Errorf("%w: dependency service id is required and must be a safe identifier", ErrInvalidDependency)
	}
	if input.DependentServiceID == input.DependencyServiceID {
		return ErrSelfDependency
	}
	if !validSingleLineText(input.Label, 160) {
		return fmt.Errorf("%w: label must be single-line UTF-8 and at most 160 bytes", ErrInvalidDependency)
	}
	return nil
}

// ValidateNodeID validates the logical topology partition used by list and
// delete operations, which do not otherwise carry a full edge input.
func ValidateNodeID(nodeID string) error {
	if !validIdentifier(strings.TrimSpace(nodeID), 128) {
		return fmt.Errorf("%w: node id is required and must be a safe identifier", ErrInvalidDependency)
	}
	return nil
}

func validIdentifier(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, runeValue := range value {
		if (runeValue >= 'a' && runeValue <= 'z') ||
			(runeValue >= 'A' && runeValue <= 'Z') ||
			(runeValue >= '0' && runeValue <= '9') ||
			runeValue == '_' || runeValue == '-' || runeValue == '.' || runeValue == ':' {
			continue
		}
		return false
	}
	return true
}

func validSingleLineText(value string, limit int) bool {
	if len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, runeValue := range value {
		if runeValue == '\n' || runeValue == '\r' || unicode.IsControl(runeValue) {
			return false
		}
	}
	return true
}
