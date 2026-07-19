// Package auth turns Tailscale Serve identity headers into short-lived browser
// sessions. The header is trusted only because production traffic reaches this
// process through the co-located Tailscale Serve proxy.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	RoleViewer          = "viewer"
	RoleAdmin           = "admin"
	maxSessions         = 1024
	maxSessionsPerLogin = 3
)

var (
	ErrMissingIdentity = errors.New("missing Tailscale identity")
	ErrInvalidSession  = errors.New("invalid session")
	ErrInvalidCSRF     = errors.New("invalid CSRF token")
	ErrInvalidOrigin   = errors.New("invalid request origin")
)

type Principal struct {
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
	Role  string `json:"role"`
}

type Session struct {
	ID       string
	CSRF     string
	Login    string
	Role     string
	Created  time.Time
	LastSeen time.Time
}

type Manager struct {
	mu       sync.Mutex
	admins   map[string]struct{}
	sessions map[string]Session
	now      func() time.Time
	secure   bool
}

func NewManager(adminLogins []string, secureCookies bool) *Manager {
	admins := make(map[string]struct{}, len(adminLogins))
	for _, login := range adminLogins {
		if normalized := normalizeLogin(login); normalized != "" {
			admins[normalized] = struct{}{}
		}
	}
	return &Manager{
		admins:   admins,
		sessions: make(map[string]Session),
		now:      time.Now,
		secure:   secureCookies,
	}
}

func (m *Manager) PrincipalFromRequest(r *http.Request) (Principal, error) {
	login := normalizeLogin(r.Header.Get("Tailscale-User-Login"))
	if login == "" {
		return Principal{}, ErrMissingIdentity
	}
	role := RoleViewer
	if _, ok := m.admins[login]; ok {
		role = RoleAdmin
	}
	return Principal{Login: login, Name: strings.TrimSpace(r.Header.Get("Tailscale-User-Name")), Role: role}, nil
}

func (m *Manager) Start(w http.ResponseWriter, principal Principal) (Session, error) {
	id, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	now := m.now().UTC()
	session := Session{ID: id, CSRF: csrf, Login: principal.Login, Role: principal.Role, Created: now, LastSeen: now}
	m.mu.Lock()
	m.removeExpiredLocked(now)
	loginSessions := 0
	for _, existing := range m.sessions {
		if existing.Login == principal.Login {
			loginSessions++
		}
	}
	if loginSessions >= maxSessionsPerLogin {
		m.removeOldestForLoginLocked(principal.Login)
	}
	if len(m.sessions) >= maxSessions {
		m.removeOldestLocked()
	}
	m.sessions[id] = session
	m.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName(),
		Value:    id,
		Path:     "/",
		Secure:   m.secure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int((24 * time.Hour).Seconds()),
	})
	return session, nil
}

func (m *Manager) Validate(r *http.Request, principal Principal) (Session, error) {
	cookie, err := r.Cookie(m.cookieName())
	if err != nil || cookie.Value == "" {
		return Session{}, ErrInvalidSession
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[cookie.Value]
	if !ok || session.Login != principal.Login || expired(session, now) {
		delete(m.sessions, cookie.Value)
		return Session{}, ErrInvalidSession
	}
	session.LastSeen = now
	m.sessions[cookie.Value] = session
	return session, nil
}

func (m *Manager) ValidateMutation(r *http.Request, principal Principal) (Session, error) {
	session, err := m.Validate(r, principal)
	if err != nil {
		return Session{}, err
	}
	if err := ValidateSameOrigin(r, m.secure); err != nil {
		return Session{}, err
	}
	provided := r.Header.Get("X-CSRF-Token")
	if len(provided) != len(session.CSRF) || subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRF)) != 1 {
		return Session{}, ErrInvalidCSRF
	}
	return session, nil
}

func ValidateSameOrigin(r *http.Request, requireHTTPS bool) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return ErrInvalidOrigin
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host != r.Host || parsed.User != nil {
		return ErrInvalidOrigin
	}
	if requireHTTPS && parsed.Scheme != "https" {
		return ErrInvalidOrigin
	}
	if !requireHTTPS && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ErrInvalidOrigin
	}
	return nil
}

func (m *Manager) cookieName() string {
	if m.secure {
		return "__Host-homelab_session"
	}
	return "homelab_session"
}

func (m *Manager) removeExpiredLocked(now time.Time) {
	for id, session := range m.sessions {
		if expired(session, now) {
			delete(m.sessions, id)
		}
	}
}

func (m *Manager) removeOldestLocked() {
	var oldestID string
	var oldest time.Time
	for id, session := range m.sessions {
		if oldestID == "" || session.LastSeen.Before(oldest) {
			oldestID, oldest = id, session.LastSeen
		}
	}
	if oldestID != "" {
		delete(m.sessions, oldestID)
	}
}

func (m *Manager) removeOldestForLoginLocked(login string) {
	var oldestID string
	var oldest time.Time
	for id, session := range m.sessions {
		if session.Login != login {
			continue
		}
		if oldestID == "" || session.LastSeen.Before(oldest) {
			oldestID, oldest = id, session.LastSeen
		}
	}
	if oldestID != "" {
		delete(m.sessions, oldestID)
	}
}

func expired(session Session, now time.Time) bool {
	return now.Sub(session.LastSeen) > 8*time.Hour || now.Sub(session.Created) > 24*time.Hour
}

func normalizeLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
