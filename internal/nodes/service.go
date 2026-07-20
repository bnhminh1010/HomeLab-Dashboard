package nodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrEnrollmentInvalid = errors.New("nodes: enrollment token is invalid or expired")
	ErrNodeUnauthorized  = errors.New("nodes: node credential is invalid")
	ErrNodeLimit         = errors.New("nodes: node limit reached")
	ErrNodeNotFound      = errors.New("nodes: node not found")
)

const (
	// MaxNodes includes the dashboard's built-in local adapter.
	MaxNodes       = 5
	maxRemoteNodes = MaxNodes - 1
)

type Node struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"displayName"`
	Hostname    string     `json:"hostname"`
	LastSeenAt  *time.Time `json:"lastSeenAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

type Enrollment struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type EnrollmentRecord struct {
	ID        string
	TokenHash [32]byte
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type NodeRecord struct {
	Node
	CredentialHash [32]byte
}

type Repository interface {
	CreateEnrollment(context.Context, EnrollmentRecord) error
	ConsumeEnrollment(context.Context, [32]byte, time.Time, NodeRecord, int) error
	GetNode(context.Context, string) (NodeRecord, error)
	ListNodes(context.Context) ([]Node, error)
	TouchNode(context.Context, string, time.Time) error
	RevokeNode(context.Context, string, time.Time) error
}

type Options struct {
	Now           func() time.Time
	Random        io.Reader
	EnrollmentTTL time.Duration
}

type Service struct {
	repository Repository
	now        func() time.Time
	random     io.Reader
	ttl        time.Duration
}

func NewService(repository Repository, options Options) (*Service, error) {
	if repository == nil {
		return nil, errors.New("nodes: repository is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.EnrollmentTTL == 0 {
		options.EnrollmentTTL = 10 * time.Minute
	}
	if options.EnrollmentTTL < time.Minute || options.EnrollmentTTL > time.Hour {
		return nil, errors.New("nodes: enrollment TTL must be between one minute and one hour")
	}
	return &Service{repository: repository, now: options.Now, random: options.Random, ttl: options.EnrollmentTTL}, nil
}

func (s *Service) CreateEnrollment(ctx context.Context, actor string) (Enrollment, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return Enrollment{}, errors.New("nodes: enrollment actor is required")
	}
	id, err := s.randomValue("enr_", 12)
	if err != nil {
		return Enrollment{}, err
	}
	token, err := s.randomValue("enroll_", 32)
	if err != nil {
		return Enrollment{}, err
	}
	now := s.now().UTC()
	record := EnrollmentRecord{
		ID: id, TokenHash: sha256.Sum256([]byte(token)), CreatedBy: actor,
		CreatedAt: now, ExpiresAt: now.Add(s.ttl),
	}
	if err := s.repository.CreateEnrollment(ctx, record); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{ID: id, Token: token, ExpiresAt: record.ExpiresAt}, nil
}

func (s *Service) Enroll(ctx context.Context, token, displayName, hostname string) (Node, string, error) {
	token = strings.TrimSpace(token)
	displayName = strings.TrimSpace(displayName)
	hostname = strings.TrimSpace(hostname)
	if token == "" || !validEnrollmentText(hostname, 255, 255) {
		return Node{}, "", ErrEnrollmentInvalid
	}
	if displayName == "" {
		displayName = hostname
	}
	if !validEnrollmentText(displayName, 320, 80) {
		return Node{}, "", ErrEnrollmentInvalid
	}
	id, err := s.randomValue("node_", 12)
	if err != nil {
		return Node{}, "", err
	}
	credential, err := s.randomValue("nodekey_", 32)
	if err != nil {
		return Node{}, "", err
	}
	now := s.now().UTC()
	node := Node{ID: id, DisplayName: displayName, Hostname: hostname, CreatedAt: now, UpdatedAt: now}
	record := NodeRecord{Node: node, CredentialHash: sha256.Sum256([]byte(credential))}
	if err := s.repository.ConsumeEnrollment(ctx, sha256.Sum256([]byte(token)), now, record, maxRemoteNodes); err != nil {
		return Node{}, "", err
	}
	return node, credential, nil
}

func validEnrollmentText(value string, maxBytes, maxRunes int) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > maxBytes || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (s *Service) Authenticate(ctx context.Context, nodeID, credential string) (Node, error) {
	nodeID, credential = strings.TrimSpace(nodeID), strings.TrimSpace(credential)
	if nodeID == "" || credential == "" {
		return Node{}, ErrNodeUnauthorized
	}
	record, err := s.repository.GetNode(ctx, nodeID)
	if err != nil || record.RevokedAt != nil {
		return Node{}, ErrNodeUnauthorized
	}
	provided := sha256.Sum256([]byte(credential))
	if subtle.ConstantTimeCompare(provided[:], record.CredentialHash[:]) != 1 {
		return Node{}, ErrNodeUnauthorized
	}
	return record.Node, nil
}

func (s *Service) List(ctx context.Context) ([]Node, error) {
	return s.repository.ListNodes(ctx)
}

func (s *Service) Touch(ctx context.Context, nodeID string) error {
	return s.repository.TouchNode(ctx, strings.TrimSpace(nodeID), s.now().UTC())
}

func (s *Service) Revoke(ctx context.Context, nodeID string) error {
	return s.repository.RevokeNode(ctx, strings.TrimSpace(nodeID), s.now().UTC())
}

func (s *Service) randomValue(prefix string, size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := io.ReadFull(s.random, buffer); err != nil {
		return "", fmt.Errorf("nodes: generate random value: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buffer), nil
}
