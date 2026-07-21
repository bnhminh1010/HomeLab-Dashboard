package healthchecks

import "time"

// CertificateObservation is the latest certificate result for a configured
// HTTPS service. It contains only inspectable certificate metadata, never the
// certificate body or private connection details.
type CertificateObservation struct {
	ServiceID string    `json:"serviceId"`
	CheckedAt time.Time `json:"checkedAt"`
	NotAfter  time.Time `json:"notAfter,omitempty"`
	Issuer    string    `json:"issuer,omitempty"`
	Error     string    `json:"error,omitempty"`
}
