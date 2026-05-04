package license

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
	"github.com/CloudNativeWorks/cnw-license-sdk/cnwlicense"
)

// CheckClaimInterval is the minimum gap between cluster-wide online re-validations.
// Combined with StartCheckLoop's ticker, this becomes the effective rate.
const CheckClaimInterval = 24 * time.Hour

// StatusView is a UI-friendly snapshot of the current license state.
type StatusView struct {
	Valid            bool       `json:"valid"`
	Plan             string     `json:"plan"`
	PlanName         string     `json:"plan_name"`
	ClientLimit      int        `json:"client_limit"`
	CurrentClients   *int64     `json:"current_clients,omitempty"`
	APIVersion       string     `json:"api_version,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	ActivatedAt      *time.Time `json:"activated_at,omitempty"`
	LastCheckedAt    *time.Time `json:"last_checked_at,omitempty"`
	LicenseKey       string     `json:"license_key,omitempty"`
	Fingerprint      string     `json:"fingerprint,omitempty"`
	ActivationID     string     `json:"activation_id,omitempty"`
	APIKeyConfigured bool       `json:"api_key_configured"`
	APIKey           string     `json:"api_key,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

// ClientLimitStatus is returned by CheckClientLimit.
type ClientLimitStatus struct {
	Plan        string
	Limit       int
	Current     int64
	IsUnlimited bool
	CanConnect  bool
}

// Service is the long-lived license manager. Safe for concurrent use.
type Service struct {
	repo   *Repo
	kek    []byte
	logger *logger.Logger

	mu          sync.RWMutex
	cached      *Info
	fingerprint string
}

// NewService constructs a Service. Call Start before any other method.
func NewService(repo *Repo, kek []byte, log *logger.Logger) *Service {
	return &Service{
		repo:   repo,
		kek:    kek,
		logger: log,
	}
}

// Start initializes the cluster-wide fingerprint and warms the in-memory cache.
// Safe to call multiple times — subsequent calls only refresh the cache.
func (s *Service) Start(ctx context.Context) {
	if err := s.ensureFingerprint(ctx); err != nil {
		s.logger.Errorf("license fingerprint init failed: %v", err)
	}
	s.refreshCache(ctx)
}

// StartRefreshLoop polls the DB at `interval` so cross-pod license changes
// (activation, API key updates) propagate without a restart.
func (s *Service) StartRefreshLoop(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshCache(ctx)
			}
		}
	}()
}

// StartCheckLoop fires CheckLicense at `interval`. CheckLicense uses TryClaimCheck
// to dedup across pods, so only one pod cluster-wide actually hits the License Server.
func (s *Service) StartCheckLoop(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.CheckLicense(ctx); err != nil {
					s.logger.Warnf("scheduled license check failed: %v", err)
				}
			}
		}
	}()
}

// IsValid reports whether the cached license currently grants the recorded plan.
// Network errors do NOT flip this to false — only an explicit server "invalid" or
// a real expiry past `expires_at` does.
func (s *Service) IsValid() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isValidLocked()
}

// GetEffectivePlan returns the plan tier currently in force. Uses cached state.
// Falls back to free if no license is activated, the server has marked it invalid,
// or expires_at has passed.
func (s *Service) GetEffectivePlan() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectivePlanLocked()
}

// GetClientLimit returns the active connection cap. 0 = unlimited.
func (s *Service) GetClientLimit() int {
	return GetClientLimit(s.GetEffectivePlan())
}

// GetCachedStatus returns a snapshot for UI rendering. Reads only from cache.
func (s *Service) GetCachedStatus() *StatusView {
	s.mu.RLock()
	defer s.mu.RUnlock()

	plan := s.effectivePlanLocked()
	view := &StatusView{
		Valid:       s.isValidLocked(),
		Plan:        plan,
		PlanName:    PlanDisplayName(plan),
		ClientLimit: GetClientLimit(plan),
		APIVersion:  version.GetProjectVersion(),
		Fingerprint: s.fingerprint,
	}
	if s.cached == nil {
		return view
	}
	view.ExpiresAt = s.cached.ExpiresAt
	view.ActivatedAt = s.cached.ActivatedAt
	view.LastCheckedAt = s.cached.LastCheckedAt
	view.ActivationID = s.cached.ActivationID
	view.Reason = s.cached.Reason
	view.LastError = s.cached.LastError
	if s.cached.LicenseKeyLast4 != "" {
		view.LicenseKey = "****" + s.cached.LicenseKeyLast4
	}
	view.APIKeyConfigured = s.cached.EncryptedAPIKey != ""
	if s.cached.APIKeyLast4 != "" {
		view.APIKey = "****" + s.cached.APIKeyLast4
	}
	return view
}

// CheckClientLimit compares the current connected count against the active plan limit.
// `countFn` runs the actual count query (typically against MongoDB). The caller decides
// whether to return ResourceExhausted, retry, etc.
func (s *Service) CheckClientLimit(ctx context.Context, countFn func(ctx context.Context) (int64, error)) (*ClientLimitStatus, error) {
	plan := s.GetEffectivePlan()
	limit := GetClientLimit(plan)
	if limit == 0 {
		return &ClientLimitStatus{Plan: plan, Limit: 0, IsUnlimited: true, CanConnect: true}, nil
	}
	count, err := countFn(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting clients: %w", err)
	}
	return &ClientLimitStatus{
		Plan:       plan,
		Limit:      limit,
		Current:    count,
		CanConnect: count < int64(limit),
	}, nil
}

// ActivateLicense validates, activates, encrypts and persists a new license key.
// Both `key` and `apiKey` are required — the activation endpoint always provisions
// the API key alongside the license key in a single call. The API key is persisted
// (encrypted) first so other pods can decrypt it for periodic re-validation.
// Refreshes the in-memory cache on success so the new plan takes effect immediately.
func (s *Service) ActivateLicense(ctx context.Context, key, apiKey string) error {
	if key == "" {
		return errors.New("license key is empty")
	}
	if apiKey == "" {
		return errors.New("api key is empty")
	}

	// Fingerprint MUST be initialized BEFORE any other write that upserts the
	// license document. SetAPIKey uses $set+upsert and would otherwise create
	// a partial document without a fingerprint field — and the bare
	// $setOnInsert path in older versions of GetOrCreateFingerprint would then
	// no-op because the document already exists. Result: empty fingerprint
	// sent to the server → "license_key and fingerprint are required" 422.
	if err := s.ensureFingerprint(ctx); err != nil {
		return fmt.Errorf("init fingerprint: %w", err)
	}
	fp := s.getFingerprint()
	if fp == "" {
		return errors.New("fingerprint initialization yielded empty value")
	}

	if err := s.SetAPIKey(ctx, apiKey); err != nil {
		return fmt.Errorf("saving api key: %w", err)
	}

	cli := s.newClient(apiKey, fp)

	valResp, err := cli.Validate(ctx, cnwlicense.ValidateRequest{
		LicenseKey:  key,
		Fingerprint: fp,
		Version:     version.GetProjectVersion(),
	})
	if err != nil {
		return fmt.Errorf("license validation failed: %s", describeSDKError(err))
	}
	if !valResp.Valid {
		return fmt.Errorf("license is not valid: %s", valResp.Reason)
	}
	// Defense in depth: don't accept a "valid" license without a plan field.
	if valResp.Plan == "" {
		return errors.New("license server returned valid response without plan")
	}

	hostname, _ := os.Hostname()
	actResp, err := cli.Activate(ctx, cnwlicense.ActivateRequest{
		LicenseKey:  key,
		Fingerprint: fp,
		Hostname:    hostname,
		OS:          runtime.GOOS,
	})
	if err != nil {
		return fmt.Errorf("license activation failed: %s", describeSDKError(err))
	}

	encryptedKey, err := Encrypt([]byte(key), s.kek)
	if err != nil {
		return fmt.Errorf("encrypting license key: %w", err)
	}

	last4 := ""
	if len(key) >= 4 {
		last4 = key[len(key)-4:]
	}

	now := time.Now().UTC()
	info := &Info{
		ID:              licenseDocID,
		Fingerprint:     fp,
		EncryptedKey:    encryptedKey,
		Valid:           true,
		Plan:            valResp.Plan,
		ExpiresAt:       valResp.ExpiresAt,
		ActivationID:    actResp.ID,
		ActivatedAt:     &actResp.ActivatedAt,
		LastCheckedAt:   &now,
		LicenseKeyLast4: last4,
		UpdatedAt:       now,
	}
	if err := s.repo.SaveOnlineActivation(ctx, info); err != nil {
		return fmt.Errorf("saving license: %w", err)
	}
	s.refreshCache(ctx)

	s.logger.Infof("license activated: plan=%s activation_id=%s expires_at=%v",
		valResp.Plan, actResp.ID, valResp.ExpiresAt)
	return nil
}

// DeleteLicense removes the persisted license entirely (hard delete).
// The installation reverts to the free plan immediately on this pod and within
// one StartRefreshLoop tick (60s default) on other pods. The fingerprint is
// regenerated on the next ActivateLicense call (deterministic from hardware,
// so usually identical to the previous one).
func (s *Service) DeleteLicense(ctx context.Context) error {
	if err := s.repo.Delete(ctx); err != nil {
		return fmt.Errorf("delete license: %w", err)
	}
	s.mu.Lock()
	s.cached = nil
	s.fingerprint = ""
	s.mu.Unlock()
	s.logger.Infof("license deleted; reverted to free plan")
	return nil
}

// SetAPIKey stores an encrypted API key for online validation. Internal helper —
// not exposed as a standalone REST endpoint; ActivateLicense calls it as part of
// the combined activation flow.
func (s *Service) SetAPIKey(ctx context.Context, apiKey string) error {
	if apiKey == "" {
		return errors.New("api key is empty")
	}
	encrypted, err := Encrypt([]byte(apiKey), s.kek)
	if err != nil {
		return fmt.Errorf("encrypting api key: %w", err)
	}
	last4 := ""
	if len(apiKey) >= 4 {
		last4 = apiKey[len(apiKey)-4:]
	}
	if err := s.repo.SaveAPIKey(ctx, encrypted, last4); err != nil {
		return fmt.Errorf("saving api key: %w", err)
	}
	s.refreshCache(ctx)
	s.logger.Infof("license api key configured (last4=%s)", last4)
	return nil
}

// CheckLicense performs a periodic re-validation against the License Server.
// Multi-pod safe via TryClaimCheck. Network failures preserve cached state
// (plan / valid / expires_at) so customers keep using the service through outages,
// AND roll back the claim so the next scheduled tick can retry instead of
// burning the full 24h slot on a single transient failure.
func (s *Service) CheckLicense(ctx context.Context) error {
	claimed, prev, err := s.repo.TryClaimCheck(ctx, CheckClaimInterval)
	if err != nil {
		return fmt.Errorf("claim check: %w", err)
	}
	if !claimed {
		return nil
	}
	claimedAt := time.Now().UTC()
	rerr := s.runValidation(ctx)
	if rerr != nil && isTransientCheckError(rerr) {
		// Best-effort rollback so the next scheduler tick can retry without
		// waiting a full CheckClaimInterval. Failure to roll back is logged
		// but not surfaced — worst case we just wait the full interval.
		if rbErr := s.repo.RestoreLastChecked(ctx, claimedAt, prev); rbErr != nil {
			s.logger.Warnf("license check claim rollback failed: %v", rbErr)
		}
	}
	return rerr
}

// ForceCheckLicense bypasses the 24h cluster-wide dedup and runs validation
// immediately. Used by the manual "Re-check now" admin endpoint so admins
// always get a fresh server response rather than a silent no-op when another
// pod claimed the daily slot.
func (s *Service) ForceCheckLicense(ctx context.Context) error {
	return s.runValidation(ctx)
}

// isTransientCheckError reports whether a runValidation error should trigger
// a TryClaimCheck rollback. Network failures and decrypt errors are transient
// (the license itself isn't necessarily invalid); anything else (Mongo write
// error, programmer error) is permanent and shouldn't grant another retry.
func isTransientCheckError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "license check failed") ||
		strings.Contains(msg, "decrypt license key")
}

// runValidation contains the shared validation logic used by both the
// periodic scheduler (via TryClaimCheck) and the manual force-check endpoint.
func (s *Service) runValidation(ctx context.Context) error {
	hostname, _ := os.Hostname()

	info, err := s.repo.Get(ctx)
	if err != nil {
		return fmt.Errorf("read license: %w", err)
	}
	if info == nil || info.EncryptedKey == "" {
		return nil
	}

	apiKey, err := s.resolveAPIKey(ctx)
	if err != nil {
		// API key decrypt failed (typically: KEK / JWT secret rotated).
		// Surface to UI via last_error so operators can see it without grep'ing logs.
		msg := "resolve api key: " + err.Error()
		s.logger.Warnf("license check skipped: %s", msg)
		if updErr := s.repo.UpdateCheckError(ctx, msg, hostname); updErr != nil {
			s.logger.Errorf("license check error update failed: %v", updErr)
		}
		s.refreshCache(ctx)
		return nil
	}
	if apiKey == "" {
		return nil
	}

	plainKey, err := Decrypt(info.EncryptedKey, s.kek)
	if err != nil {
		// License key decrypt failed (KEK rotation / corrupted ciphertext).
		// Same operator-visibility concern as above.
		msg := "decrypt license key: " + err.Error()
		s.logger.Errorf("license check failed: %s", msg)
		if updErr := s.repo.UpdateCheckError(ctx, msg, hostname); updErr != nil {
			s.logger.Errorf("license check error update failed: %v", updErr)
		}
		s.refreshCache(ctx)
		return fmt.Errorf("%s", msg)
	}
	defer zeroBytes(plainKey)

	fp := s.getFingerprint()
	cli := s.newClient(apiKey, fp)

	valResp, err := cli.Validate(ctx, cnwlicense.ValidateRequest{
		LicenseKey:  string(plainKey),
		Fingerprint: fp,
		Version:     version.GetProjectVersion(),
	})
	if err != nil {
		s.logger.Warnf("license check network error (cached state preserved): %s", describeSDKError(err))
		if updErr := s.repo.UpdateCheckError(ctx, describeSDKError(err), hostname); updErr != nil {
			s.logger.Errorf("license check error update failed: %v", updErr)
		}
		s.refreshCache(ctx)
		return fmt.Errorf("license check failed: %s", describeSDKError(err))
	}

	// Defense in depth: server-side bug could return Valid:true with empty plan.
	// Treat empty plan as invalid to avoid silently downgrading the customer.
	if valResp.Valid && valResp.Plan == "" {
		s.logger.Warnf("license server returned valid:true with empty plan; treating as invalid")
		valResp.Valid = false
		if valResp.Reason == "" {
			valResp.Reason = "server returned empty plan"
		}
	}

	if err := s.repo.UpdateCheckResult(ctx, valResp.Valid, valResp.Reason, valResp.Plan, valResp.ExpiresAt, "", hostname); err != nil {
		return fmt.Errorf("update check result: %w", err)
	}
	if !valResp.Valid {
		s.logger.Warnf("license server marked license invalid: %s", valResp.Reason)
	} else {
		s.logger.Infof("license re-validated: plan=%s expires_at=%v", valResp.Plan, valResp.ExpiresAt)
	}
	s.refreshCache(ctx)
	return nil
}

// --- internal helpers ---

func (s *Service) isValidLocked() bool {
	if s.cached == nil {
		return false
	}
	if !s.cached.Valid {
		return false
	}
	if s.cached.ExpiresAt != nil && s.cached.ExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}

func (s *Service) effectivePlanLocked() string {
	if s.cached == nil {
		return PlanFree
	}
	if !s.cached.Valid {
		return PlanFree
	}
	if s.cached.ExpiresAt != nil && s.cached.ExpiresAt.Before(time.Now()) {
		return PlanFree
	}
	return ResolvePlan(s.cached.Plan)
}

func (s *Service) refreshCache(ctx context.Context) {
	info, err := s.repo.Get(ctx)
	if err != nil {
		s.logger.Errorf("license cache refresh failed: %v", err)
		return
	}
	s.mu.Lock()
	s.cached = info
	if info != nil && info.Fingerprint != "" {
		s.fingerprint = info.Fingerprint
	}
	s.mu.Unlock()
}

func (s *Service) ensureFingerprint(ctx context.Context) error {
	if s.getFingerprint() != "" {
		return nil
	}
	local, err := cnwlicense.GenerateFingerprint()
	if err != nil {
		return fmt.Errorf("generate fingerprint: %w", err)
	}
	stored, err := s.repo.GetOrCreateFingerprint(ctx, local)
	if err != nil {
		return fmt.Errorf("persist fingerprint: %w", err)
	}
	s.mu.Lock()
	s.fingerprint = stored
	s.mu.Unlock()
	s.logger.Infof("license fingerprint initialized: %s", stored)
	return nil
}

func (s *Service) getFingerprint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fingerprint
}

// resolveAPIKey returns the API key to use. Only source is the encrypted DB
// value (set via POST /license/activate). No config / env / build-time fallback
// — those would be license bypass vectors and we deliberately closed them.
func (s *Service) resolveAPIKey(ctx context.Context) (string, error) {
	info, err := s.repo.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("read license: %w", err)
	}
	if info == nil || info.EncryptedAPIKey == "" {
		return "", nil
	}
	plain, err := Decrypt(info.EncryptedAPIKey, s.kek)
	if err != nil {
		return "", fmt.Errorf("decrypt api key: %w", err)
	}
	out := string(plain)
	zeroBytes(plain)
	return out, nil
}

func (s *Service) newClient(apiKey, fingerprint string) *cnwlicense.OnlineClient {
	opts := []cnwlicense.ClientOption{cnwlicense.WithFingerprint(fingerprint)}
	// CNW License Server validates metadata keys strictly and rejects unknown keys
	// (or non-semver versions) with VALIDATION_ERROR. Mirror certautopilot's shape:
	// only send {version: <semver>} and only when we actually have a real semver.
	if v := semverOrEmpty(version.GetProjectVersion()); v != "" {
		opts = append(opts, cnwlicense.WithMetadata(map[string]string{"version": v}))
	}
	return cnwlicense.NewOnlineClient(ServerURL, apiKey, opts...)
}

// describeSDKError unwraps a cnwlicense.ServerError if present so the operator
// sees the real server message ("license expired", "fingerprint mismatch",
// "metadata.X required", etc.) instead of the bare sentinel ("invalid metadata").
func describeSDKError(err error) string {
	if err == nil {
		return ""
	}
	var se *cnwlicense.ServerError
	if errors.As(err, &se) {
		return fmt.Sprintf("%s [code=%s, http=%d]", se.Message, se.Code, se.StatusCode)
	}
	return err.Error()
}

// semverOrEmpty returns v if it looks like a semver (starts with a digit),
// otherwise empty. "dev", "" and other non-semver markers map to empty so
// we don't ship a bogus version string to the license server.
func semverOrEmpty(v string) string {
	if v == "" {
		return ""
	}
	if v[0] >= '0' && v[0] <= '9' {
		return v
	}
	return ""
}

// zeroBytes overwrites a byte slice in place. Used to scrub decrypted secrets
// (license key, api key) from memory after use so a heap dump can't recover them.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
