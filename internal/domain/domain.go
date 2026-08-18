// Package domain manages custom application domains: validation, storage,
// DNS verification, and activation of proxy routes once a domain resolves to
// the server and its application is running (PRD §7.6).
package domain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SpritexAI/SpritexDock/internal/application"
	"github.com/SpritexAI/SpritexDock/internal/db"
	"github.com/SpritexAI/SpritexDock/internal/deployment"
	"github.com/SpritexAI/SpritexDock/internal/proxy"
)

// Verification states stored in domains.verification_status.
const (
	StatusPending  = "pending"
	StatusVerified = "verified"
	StatusFailed   = "failed"
)

const domainTypeCustom = "custom"

var (
	// ErrNotFound signals a missing application or domain record.
	ErrNotFound = errors.New("domain not found")
	// ErrDuplicateDomain signals the hostname is already configured.
	ErrDuplicateDomain = errors.New("domain already exists")
	// ErrInvalidHostname signals an untrusted or malformed hostname.
	ErrInvalidHostname = errors.New("invalid domain")
)

// Domain is the persisted custom-domain record for an application.
type Domain struct {
	ID                    string  `json:"id"`
	ApplicationID         string  `json:"application_id"`
	Hostname              string  `json:"hostname"`
	Type                  string  `json:"type"`
	VerificationStatus    string  `json:"verification_status"`
	CertificateStatus     *string `json:"certificate_status,omitempty"`
	LastVerificationError *string `json:"last_verification_error,omitempty"`
	VerifiedAt            *string `json:"verified_at,omitempty"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
}

// DNSRecord describes the record the domain owner must create before the
// domain can be verified.
type DNSRecord struct {
	RecordType string `json:"record_type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
}

// RequiredDNSRecord returns the A record pointing a custom hostname at this
// server's public IP.
func RequiredDNSRecord(hostname, publicIP string) DNSRecord {
	return DNSRecord{RecordType: "A", Name: hostname, Value: publicIP}
}

// NormalizeHostname validates untrusted domain input and returns the
// normalized lowercase hostname. Hostnames in the generated sslip.io
// namespace and IP literals are rejected; generated hostnames are managed by
// SpritexDock, not configured as custom domains.
func NormalizeHostname(raw string) (string, error) {
	hostname := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), ".")))
	if hostname == "" || len(hostname) > 253 {
		return "", fmt.Errorf("%w: hostname must be 1-253 characters", ErrInvalidHostname)
	}
	if !strings.Contains(hostname, ".") {
		return "", fmt.Errorf("%w: hostname must include a parent domain", ErrInvalidHostname)
	}
	if net.ParseIP(hostname) != nil {
		return "", fmt.Errorf("%w: IP literals are not supported", ErrInvalidHostname)
	}
	if hostname == "sslip.io" || strings.HasSuffix(hostname, ".sslip.io") {
		return "", fmt.Errorf("%w: hostnames under sslip.io are reserved for generated URLs", ErrInvalidHostname)
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) < 1 || len(label) > 63 {
			return "", fmt.Errorf("%w: label lengths must be 1-63 characters", ErrInvalidHostname)
		}
		if !labelAllowed(label) {
			return "", fmt.Errorf("%w: hostname contains disallowed characters", ErrInvalidHostname)
		}
	}
	return hostname, nil
}

func labelAllowed(label string) bool {
	for i, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-':
			if i == 0 || i == len(label)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Add stores a new pending custom domain for an application.
func Add(ctx context.Context, state *db.DB, applicationID, rawHostname string) (*Domain, error) {
	hostname, err := NormalizeHostname(rawHostname)
	if err != nil {
		return nil, err
	}
	if _, err := application.Get(ctx, state, applicationID); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	item := &Domain{
		ID:                 uuid.NewString(),
		ApplicationID:      applicationID,
		Hostname:           hostname,
		Type:               domainTypeCustom,
		VerificationStatus: StatusPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	_, err = state.ExecContext(ctx, `
		INSERT INTO domains
		(id, application_id, hostname, type, verification_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, item.ID, item.ApplicationID, item.Hostname, item.Type, item.VerificationStatus, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: domains.hostname") {
			return nil, ErrDuplicateDomain
		}
		return nil, fmt.Errorf("add domain: %w", err)
	}
	return item, nil
}

// List returns the application's custom domains ordered by hostname.
func List(ctx context.Context, state *db.DB, applicationID string) ([]*Domain, error) {
	rows, err := state.QueryContext(ctx, domainSelect+" WHERE application_id = ? ORDER BY hostname ASC", applicationID)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []*Domain
	for rows.Next() {
		item := &Domain{}
		if err := scanDomain(rows, item); err != nil {
			return nil, fmt.Errorf("scan domain: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate domains: %w", err)
	}
	return items, nil
}

// ListVerified returns the hostnames of the application's verified custom
// domains ordered by hostname.
func ListVerified(ctx context.Context, state *db.DB, applicationID string) ([]string, error) {
	rows, err := state.QueryContext(ctx, `
		SELECT hostname FROM domains
		WHERE application_id = ? AND verification_status = ?
		ORDER BY hostname ASC
	`, applicationID, StatusVerified)
	if err != nil {
		return nil, fmt.Errorf("list verified domains: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var hostnames []string
	for rows.Next() {
		var hostname string
		if err := rows.Scan(&hostname); err != nil {
			return nil, fmt.Errorf("scan verified domain: %w", err)
		}
		hostnames = append(hostnames, hostname)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verified domains: %w", err)
	}
	return hostnames, nil
}

// Get returns one custom domain by application and hostname.
func Get(ctx context.Context, state *db.DB, applicationID, rawHostname string) (*Domain, error) {
	hostname, err := NormalizeHostname(rawHostname)
	if err != nil {
		return nil, err
	}
	item := &Domain{}
	err = scanDomain(state.QueryRowContext(ctx, domainSelect+" WHERE application_id = ? AND hostname = ?", applicationID, hostname), item)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get domain: %w", err)
	}
	return item, nil
}

// Delete removes the domain record. The caller is responsible for removing
// any active proxy route.
func Delete(ctx context.Context, state *db.DB, applicationID, rawHostname string) error {
	hostname, err := NormalizeHostname(rawHostname)
	if err != nil {
		return err
	}
	result, err := state.ExecContext(ctx, "DELETE FROM domains WHERE application_id = ? AND hostname = ?", applicationID, hostname)
	if err != nil {
		return fmt.Errorf("delete domain: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete domain: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolver looks up host addresses; *net.Resolver satisfies it.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Service coordinates domain verification with proxy-route activation.
type Service struct {
	State    *db.DB
	PublicIP string
	Router   proxy.Router // optional; nil disables route activation
	Resolver Resolver     // optional; defaults to net.DefaultResolver
}

// VerifyDomain checks that the domain resolves to the configured public IP,
// persists the outcome, and — when verification succeeds and the application
// is currently running — activates its proxy route (PRD §7.6: a domain is not
// activated until the target application runs and the route is valid). The
// returned error carries the verification failure; callers can still inspect
// the returned record.
func (s *Service) VerifyDomain(ctx context.Context, applicationID, rawHostname string) (*Domain, error) {
	item, err := Get(ctx, s.State, applicationID, rawHostname)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(s.PublicIP) == "" {
		return s.recordFailure(ctx, item, "server public IP is not configured")
	}

	resolver := s.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupHost(ctx, item.Hostname)
	if err != nil {
		return s.recordFailure(ctx, item, fmt.Sprintf("DNS lookup failed: %v", err))
	}
	for _, address := range addresses {
		if address == s.PublicIP {
			return s.recordSuccess(ctx, item)
		}
	}
	sort.Strings(addresses)
	return s.recordFailure(ctx, item, fmt.Sprintf("domain resolves to [%s], expected %s", strings.Join(addresses, ", "), s.PublicIP))
}

func (s *Service) recordSuccess(ctx context.Context, item *Domain) (*Domain, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.State.ExecContext(ctx, `
		UPDATE domains
		SET verification_status = ?, verified_at = ?, last_verification_error = NULL, updated_at = ?
		WHERE id = ?
	`, StatusVerified, now, now, item.ID)
	if err != nil {
		return item, fmt.Errorf("mark domain verified: %w", err)
	}
	item.VerificationStatus = StatusVerified
	item.VerifiedAt = &now
	item.LastVerificationError = nil
	item.UpdatedAt = now

	if s.Router != nil {
		if err := s.activateIfRunning(ctx, item.ApplicationID, item.Hostname); err != nil {
			return item, err
		}
	}
	return item, nil
}

func (s *Service) recordFailure(ctx context.Context, item *Domain, reason string) (*Domain, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.State.ExecContext(ctx, `
		UPDATE domains
		SET verification_status = ?, last_verification_error = ?, updated_at = ?
		WHERE id = ?
	`, StatusFailed, reason, now, item.ID)
	if err != nil {
		return item, fmt.Errorf("mark domain failed: %w", err)
	}
	item.VerificationStatus = StatusFailed
	reasonCopy := reason
	item.LastVerificationError = &reasonCopy
	item.UpdatedAt = now
	return item, errors.New(reason)
}

// activateIfRunning routes hostname to the application's exposed port once
// its current deployment is running with a live container.
func (s *Service) activateIfRunning(ctx context.Context, applicationID, hostname string) error {
	app, err := application.Get(ctx, s.State, applicationID)
	if err != nil {
		return err
	}
	if app.CurrentDeploymentID == nil || *app.CurrentDeploymentID == "" {
		return nil
	}
	current, err := deployment.Get(ctx, s.State, *app.CurrentDeploymentID)
	if err != nil {
		return err
	}
	if current.Status != deployment.StatusRunning || current.ContainerID == nil || *current.ContainerID == "" {
		return nil
	}
	return s.Router.AddRoute(ctx, hostname, app.ExposedPort)
}

// ActivateVerifiedRoutes adds proxy routes for all of the application's
// verified custom domains, targeting targetPort. It is called by the
// deployment worker after a successful deployment.
func ActivateVerifiedRoutes(ctx context.Context, state *db.DB, router proxy.Router, applicationID string, targetPort int) error {
	hostnames, err := ListVerified(ctx, state, applicationID)
	if err != nil {
		return err
	}
	var errs []error
	for _, hostname := range hostnames {
		if err := router.AddRoute(ctx, hostname, targetPort); err != nil {
			errs = append(errs, fmt.Errorf("route %s: %w", hostname, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("activate custom domain routes: %v", errs)
	}
	return nil
}

const domainSelect = `
	SELECT id, application_id, hostname, type, verification_status,
	       certificate_status_metadata, last_verification_error, verified_at, created_at, updated_at
	FROM domains`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDomain(row rowScanner, item *Domain) error {
	return row.Scan(&item.ID, &item.ApplicationID, &item.Hostname, &item.Type, &item.VerificationStatus,
		&item.CertificateStatus, &item.LastVerificationError, &item.VerifiedAt, &item.CreatedAt, &item.UpdatedAt)
}
