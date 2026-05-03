package license

import (
	"context"
	"errors"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	// licenseDocID is the singleton document ID; we keep exactly one license document.
	licenseDocID = "installation"
)

// Info is the persisted license state in MongoDB. Online mode only.
type Info struct {
	ID              string     `bson:"_id"`
	Fingerprint     string     `bson:"fingerprint"`
	EncryptedKey    string     `bson:"encrypted_key,omitempty"`
	EncryptedAPIKey string     `bson:"encrypted_api_key,omitempty"`
	LicenseKeyLast4 string     `bson:"license_key_last4,omitempty"`
	APIKeyLast4     string     `bson:"api_key_last4,omitempty"`
	Valid           bool       `bson:"valid"`
	Plan            string     `bson:"plan,omitempty"`
	ExpiresAt       *time.Time `bson:"expires_at,omitempty"`
	ActivationID    string     `bson:"activation_id,omitempty"`
	ActivatedAt     *time.Time `bson:"activated_at,omitempty"`
	LastCheckedAt   *time.Time `bson:"last_checked_at,omitempty"`
	LastCheckHost   string     `bson:"last_check_host,omitempty"`
	Reason          string     `bson:"reason,omitempty"`
	LastError       string     `bson:"last_error,omitempty"`
	UpdatedAt       time.Time  `bson:"updated_at"`
}

// Repo is the MongoDB-backed license store.
type Repo struct {
	col *mongo.Collection
}

func NewRepo(ctx *db.AppContext) *Repo {
	return &Repo{col: ctx.Client.Collection("license")}
}

// GetOrCreateFingerprint returns the stored fingerprint, writing the supplied
// one if either the document doesn't exist or it exists but has no fingerprint
// field (e.g. SetAPIKey upserted a partial document before fingerprint init).
// Hardware-derived fingerprints are deterministic, so even with a race between
// pods the written value converges to the same string.
func (r *Repo) GetOrCreateFingerprint(ctx context.Context, fingerprint string) (string, error) {
	var existing Info
	err := r.col.FindOne(ctx, bson.M{"_id": licenseDocID}).Decode(&existing)
	if err == nil && existing.Fingerprint != "" {
		return existing.Fingerprint, nil
	}
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return "", err
	}

	now := time.Now().UTC()
	update := bson.M{
		"$set": bson.M{
			"fingerprint": fingerprint,
			"updated_at":  now,
		},
		"$setOnInsert": bson.M{
			"valid": false,
		},
	}
	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var info Info
	if err := r.col.FindOneAndUpdate(ctx, bson.M{"_id": licenseDocID}, update, opts).Decode(&info); err != nil {
		return "", err
	}
	return info.Fingerprint, nil
}

// Get returns the singleton license document, or (nil, nil) if it doesn't exist.
func (r *Repo) Get(ctx context.Context) (*Info, error) {
	var info Info
	err := r.col.FindOne(ctx, bson.M{"_id": licenseDocID}).Decode(&info)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// SaveOnlineActivation writes activation results, preserving API key fields.
func (r *Repo) SaveOnlineActivation(ctx context.Context, info *Info) error {
	now := time.Now().UTC()
	set := bson.M{
		"fingerprint":       info.Fingerprint,
		"encrypted_key":     info.EncryptedKey,
		"valid":             info.Valid,
		"plan":              info.Plan,
		"expires_at":        info.ExpiresAt,
		"reason":            info.Reason,
		"activation_id":     info.ActivationID,
		"activated_at":      info.ActivatedAt,
		"last_checked_at":   info.LastCheckedAt,
		"last_error":        info.LastError,
		"license_key_last4": info.LicenseKeyLast4,
		"updated_at":        now,
	}
	_, err := r.col.UpdateOne(
		ctx,
		bson.M{"_id": licenseDocID},
		bson.M{"$set": set},
		options.Update().SetUpsert(true),
	)
	return err
}

// SaveAPIKey stores an encrypted API key alongside its last-4 hint.
func (r *Repo) SaveAPIKey(ctx context.Context, encrypted, last4 string) error {
	now := time.Now().UTC()
	set := bson.M{
		"encrypted_api_key": encrypted,
		"api_key_last4":     last4,
		"updated_at":        now,
	}
	_, err := r.col.UpdateOne(
		ctx,
		bson.M{"_id": licenseDocID},
		bson.M{"$set": set},
		options.Update().SetUpsert(true),
	)
	return err
}

// UpdateCheckResult writes a successful re-validation result authoritatively.
func (r *Repo) UpdateCheckResult(ctx context.Context, valid bool, reason, plan string, expiresAt *time.Time, lastError, host string) error {
	now := time.Now().UTC()
	set := bson.M{
		"valid":           valid,
		"reason":          reason,
		"plan":            plan,
		"expires_at":      expiresAt,
		"last_checked_at": now,
		"last_check_host": host,
		"last_error":      lastError,
		"updated_at":      now,
	}
	_, err := r.col.UpdateOne(ctx, bson.M{"_id": licenseDocID}, bson.M{"$set": set})
	return err
}

// UpdateCheckError records a network/transient failure WITHOUT touching valid/plan/expires_at.
// This preserves the cached license so the customer keeps using the service until the
// real expires_at — network outages do not downgrade the plan.
func (r *Repo) UpdateCheckError(ctx context.Context, errMsg, host string) error {
	now := time.Now().UTC()
	set := bson.M{
		"last_checked_at": now,
		"last_check_host": host,
		"last_error":      errMsg,
		"updated_at":      now,
	}
	_, err := r.col.UpdateOne(ctx, bson.M{"_id": licenseDocID}, bson.M{"$set": set})
	return err
}

// Delete removes the singleton license document entirely. Used by the
// admin "delete license" endpoint to reset the installation back to free.
// Returns nil if the document didn't exist (idempotent).
func (r *Repo) Delete(ctx context.Context) error {
	_, err := r.col.DeleteOne(ctx, bson.M{"_id": licenseDocID})
	return err
}

// TryClaimCheck atomically reserves the next license re-validation slot.
// Only one pod across the cluster wins per `interval`; others get false and
// skip the API call. Returns the previous last_checked_at value (nil if unset)
// so the caller can roll back the claim on a transient network failure via
// RestoreLastChecked — without that, a single failed call would burn the
// 24h slot and block all retries for a day.
func (r *Repo) TryClaimCheck(ctx context.Context, interval time.Duration) (claimed bool, prev *time.Time, err error) {
	now := time.Now().UTC()
	filter := bson.M{
		"_id": licenseDocID,
		"$or": bson.A{
			bson.M{"last_checked_at": bson.M{"$lt": now.Add(-interval)}},
			bson.M{"last_checked_at": nil},
			bson.M{"last_checked_at": bson.M{"$exists": false}},
		},
	}
	update := bson.M{"$set": bson.M{"last_checked_at": now}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.Before)

	var before Info
	err = r.col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&before)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return true, before.LastCheckedAt, nil
}

// RestoreLastChecked rolls last_checked_at back to a previous value (or unsets
// it when prev is nil). Called by the service after a transient network failure
// so the next TryClaimCheck can re-attempt instead of waiting a full interval.
// Uses a guarded UpdateOne — only writes if last_checked_at hasn't been bumped
// further by another check in the meantime.
func (r *Repo) RestoreLastChecked(ctx context.Context, claimedAt time.Time, prev *time.Time) error {
	filter := bson.M{
		"_id":             licenseDocID,
		"last_checked_at": claimedAt,
	}
	var update bson.M
	if prev == nil {
		update = bson.M{"$unset": bson.M{"last_checked_at": ""}}
	} else {
		update = bson.M{"$set": bson.M{"last_checked_at": prev}}
	}
	_, err := r.col.UpdateOne(ctx, filter, update)
	return err
}
