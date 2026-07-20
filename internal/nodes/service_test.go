package nodes

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentIsSingleUseAndCredentialsAreNodeScoped(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, err := NewService(repository, Options{
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(testRandomBytes(512)),
	})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := service.CreateEnrollment(context.Background(), "admin@example.com")
	if err != nil || enrollment.ExpiresAt.Sub(now) != 10*time.Minute {
		t.Fatalf("CreateEnrollment() = %#v, %v", enrollment, err)
	}
	node, credential, err := service.Enroll(context.Background(), enrollment.Token, "Compute One", "compute-1")
	if err != nil || node.ID == "" || credential == "" {
		t.Fatalf("Enroll() = %#v, %q, %v", node, credential, err)
	}
	if _, _, err := service.Enroll(context.Background(), enrollment.Token, "Again", "compute-2"); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("reused enrollment error = %v", err)
	}
	authenticated, err := service.Authenticate(context.Background(), node.ID, credential)
	if err != nil || authenticated.Hostname != "compute-1" {
		t.Fatalf("Authenticate() = %#v, %v", authenticated, err)
	}
	if _, err := service.Authenticate(context.Background(), node.ID, credential+"bad"); !errors.Is(err, ErrNodeUnauthorized) {
		t.Fatalf("bad credential error = %v", err)
	}
	if err := service.Revoke(context.Background(), node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(context.Background(), node.ID, credential); !errors.Is(err, ErrNodeUnauthorized) {
		t.Fatalf("revoked credential error = %v", err)
	}
}

func TestEnrollmentExpiresAndNodeLimitIsEnforced(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(testRandomBytes(4096)),
	})
	expired, _ := service.CreateEnrollment(context.Background(), "admin")
	now = now.Add(11 * time.Minute)
	if _, _, err := service.Enroll(context.Background(), expired.Token, "expired", "expired"); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("expired enrollment error = %v", err)
	}
	for index := 0; index < maxRemoteNodes; index++ {
		enrollment, _ := service.CreateEnrollment(context.Background(), "admin")
		if _, _, err := service.Enroll(context.Background(), enrollment.Token, "node", "node"); err != nil {
			t.Fatal(err)
		}
	}
	enrollment, _ := service.CreateEnrollment(context.Background(), "admin")
	if _, _, err := service.Enroll(context.Background(), enrollment.Token, "too many", "too-many"); !errors.Is(err, ErrNodeLimit) {
		t.Fatalf("node limit error = %v", err)
	}
}

func TestEnrollmentRejectsControlCharactersAndAcceptsRuneBoundedDisplayName(t *testing.T) {
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{Random: bytes.NewReader(testRandomBytes(2048))})
	for _, input := range []struct {
		displayName string
		hostname    string
	}{
		{displayName: "rack\nforged", hostname: "rack-1"},
		{displayName: "rack", hostname: "rack\rforged"},
		{displayName: string([]byte{0xff}), hostname: "rack-1"},
	} {
		enrollment, err := service.CreateEnrollment(context.Background(), "admin")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.Enroll(context.Background(), enrollment.Token, input.displayName, input.hostname); !errors.Is(err, ErrEnrollmentInvalid) {
			t.Fatalf("Enroll(%q, %q) error = %v", input.displayName, input.hostname, err)
		}
	}

	enrollment, err := service.CreateEnrollment(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	displayName := strings.Repeat("máy", 20)
	if _, _, err := service.Enroll(context.Background(), enrollment.Token, displayName, "rack-utf8"); err != nil {
		t.Fatalf("valid UTF-8 display name rejected: %v", err)
	}
}

func testRandomBytes(size int) []byte {
	result := make([]byte, size)
	for index := range result {
		result[index] = byte(index*31 + 7)
	}
	return result
}

type memoryRepository struct {
	enrollments map[[32]byte]EnrollmentRecord
	nodes       map[string]NodeRecord
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{enrollments: make(map[[32]byte]EnrollmentRecord), nodes: make(map[string]NodeRecord)}
}

func (r *memoryRepository) CreateEnrollment(_ context.Context, record EnrollmentRecord) error {
	r.enrollments[record.TokenHash] = record
	return nil
}

func (r *memoryRepository) ConsumeEnrollment(_ context.Context, hash [32]byte, now time.Time, node NodeRecord, limit int) error {
	record, ok := r.enrollments[hash]
	if !ok || !now.Before(record.ExpiresAt) {
		return ErrEnrollmentInvalid
	}
	if len(r.nodes) >= limit {
		return ErrNodeLimit
	}
	delete(r.enrollments, hash)
	r.nodes[node.ID] = node
	return nil
}

func (r *memoryRepository) GetNode(_ context.Context, id string) (NodeRecord, error) {
	node, ok := r.nodes[id]
	if !ok {
		return NodeRecord{}, ErrNodeNotFound
	}
	return node, nil
}

func (r *memoryRepository) ListNodes(context.Context) ([]Node, error) {
	result := make([]Node, 0, len(r.nodes))
	for _, node := range r.nodes {
		result = append(result, node.Node)
	}
	return result, nil
}

func (r *memoryRepository) TouchNode(_ context.Context, id string, now time.Time) error {
	node, ok := r.nodes[id]
	if !ok {
		return ErrNodeNotFound
	}
	node.LastSeenAt = &now
	node.UpdatedAt = now
	r.nodes[id] = node
	return nil
}

func (r *memoryRepository) RevokeNode(_ context.Context, id string, now time.Time) error {
	node, ok := r.nodes[id]
	if !ok {
		return ErrNodeNotFound
	}
	node.RevokedAt = &now
	node.UpdatedAt = now
	r.nodes[id] = node
	return nil
}
