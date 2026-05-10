package settings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/syslog"
)

// syslogSettingsProject is the sentinel project value used to store the
// global audit-syslog forwarding configuration. Real project identifiers
// never start with double underscores, so this lives alongside per-project
// rows in the same collection without collision.
const syslogSettingsProject = "__global_syslog__"

// redactSentinel is what GET responses return in place of real PEM/key
// values; PUT requests round-trip the sentinel to mean "leave unchanged".
const redactSentinel = "***REDACTED***"

// validProtocols is the closed enum accepted by Set/Update.
var validProtocols = map[string]bool{
	syslog.ProtocolUDP:    true,
	syslog.ProtocolTCP:    true,
	syslog.ProtocolTCPTLS: true,
}

// validFacilities is the closed enum accepted in Facility.
var validFacilities = map[string]bool{
	"":       true, // empty falls back to local0
	"local0": true,
	"local1": true,
	"local2": true,
	"local3": true,
	"local4": true,
	"local5": true,
	"local6": true,
	"local7": true,
}

// GetSyslogConfig returns the current syslog forwarding configuration with
// TLS material masked. Has* booleans tell the UI whether secrets exist
// without leaking their content.
func (handler *AppHandler) GetSyslogConfig(c *gin.Context) {
	cfg, err := handler.loadSyslogConfig(c.Request.Context())
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusOK, gin.H{"syslog_config": nil})
			return
		}
		handler.Logger.Errorf("Failed to load syslog config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to load syslog config"})
		return
	}

	maskSyslogConfigForResponse(cfg)
	c.JSON(http.StatusOK, gin.H{"syslog_config": cfg})
}

// SetSyslogConfig creates or fully replaces the syslog forwarding config.
func (handler *AppHandler) SetSyslogConfig(c *gin.Context) {
	handler.upsertSyslogConfig(c)
}

// UpdateSyslogConfig is the same upsert used by SetSyslogConfig, with the
// preserve-on-empty semantics. Provided as its own handler so callers can
// distinguish create vs. update at the route level (audit reads the verb).
func (handler *AppHandler) UpdateSyslogConfig(c *gin.Context) {
	handler.upsertSyslogConfig(c)
}

func (handler *AppHandler) upsertSyslogConfig(c *gin.Context) {
	var cfg models.SyslogConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body: " + err.Error()})
		return
	}

	// Strip read-only flags so they cannot be persisted from the client.
	cfg.HasCACert = false
	cfg.HasClientCert = false
	cfg.HasClientKey = false

	cfg.Protocol = strings.TrimSpace(strings.ToLower(cfg.Protocol))
	cfg.Facility = strings.TrimSpace(strings.ToLower(cfg.Facility))
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Tag = strings.TrimSpace(cfg.Tag)

	if err := validateSyslogConfig(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	ctx := c.Request.Context()
	existing, err := handler.loadSyslogConfig(ctx)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		handler.Logger.Errorf("Failed to load existing syslog config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to load syslog config"})
		return
	}

	preserveSecretsOnEmpty(&cfg, existing)

	settingsCollection := handler.Context.Client.Collection("settings")
	filter := bson.M{"project": syslogSettingsProject}
	update := bson.M{"$set": bson.M{"syslog_config": cfg}}
	opts := options.Update().SetUpsert(true)

	if _, err := settingsCollection.UpdateOne(ctx, filter, update, opts); err != nil {
		handler.Logger.Errorf("Failed to save syslog config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to save syslog config"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Syslog config saved"})
}

// DeleteSyslogConfig removes the stored config entirely. The forwarder will
// pick up the deletion on its next config-poll tick (≤30s).
func (handler *AppHandler) DeleteSyslogConfig(c *gin.Context) {
	ctx := c.Request.Context()
	settingsCollection := handler.Context.Client.Collection("settings")

	filter := bson.M{"project": syslogSettingsProject}
	update := bson.M{"$unset": bson.M{"syslog_config": ""}}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		handler.Logger.Errorf("Failed to delete syslog config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to delete syslog config"})
		return
	}
	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "Syslog config not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Syslog config deleted"})
}

// TestSyslogConnection opens a short-lived client with the supplied (or
// stored) config and sends one synthetic frame. Returns success + latency
// so the operator can validate the SIEM endpoint before enabling.
func (handler *AppHandler) TestSyslogConnection(c *gin.Context) {
	var req models.SyslogConfig
	useStored := false
	if err := c.ShouldBindJSON(&req); err != nil || req.Host == "" {
		// Empty body / bind failure → fall back to the persisted config.
		useStored = true
	}

	ctx := c.Request.Context()
	if useStored {
		stored, err := handler.loadSyslogConfig(ctx)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "No stored syslog config to test"})
			return
		}
		req = *stored
	} else {
		// Fill in any masked secrets from the stored config.
		stored, err := handler.loadSyslogConfig(ctx)
		if err == nil {
			preserveSecretsOnEmpty(&req, stored)
		}
		req.Protocol = strings.TrimSpace(strings.ToLower(req.Protocol))
		if err := validateSyslogConfig(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
	}

	cfg := syslog.Config{
		Protocol:         req.Protocol,
		Host:             req.Host,
		Port:             req.Port,
		CACert:           req.CACert,
		ClientCert:       req.ClientCert,
		ClientKey:        req.ClientKey,
		ConnectTimeoutMs: req.ConnectTimeoutMs,
		WriteTimeoutMs:   req.WriteTimeoutMs,
	}

	start := time.Now()
	if err := syslog.TestConnection(cfg); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success":    false,
			"error":      err.Error(),
			"latency_ms": time.Since(start).Milliseconds(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"latency_ms": time.Since(start).Milliseconds(),
	})
}

// LoadSyslogConfig is the SettingsLoader implementation consumed by the
// pkg/syslog forwarder. Returns nil when no config has been saved.
func (handler *AppHandler) LoadSyslogConfig(ctx context.Context) (*syslog.ForwarderConfig, error) {
	cfg, err := handler.loadSyslogConfig(ctx)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &syslog.ForwarderConfig{
		Enabled:          cfg.Enabled,
		Protocol:         cfg.Protocol,
		Host:             cfg.Host,
		Port:             cfg.Port,
		Facility:         cfg.Facility,
		Tag:              cfg.Tag,
		CACert:           cfg.CACert,
		ClientCert:       cfg.ClientCert,
		ClientKey:        cfg.ClientKey,
		QueueSize:        cfg.QueueSize,
		ConnectTimeoutMs: cfg.ConnectTimeoutMs,
		WriteTimeoutMs:   cfg.WriteTimeoutMs,
	}, nil
}

// loadSyslogConfig fetches the persisted syslog config (raw, with secrets).
func (handler *AppHandler) loadSyslogConfig(ctx context.Context) (*models.SyslogConfig, error) {
	settingsCollection := handler.Context.Client.Collection("settings")
	var doc models.Settings
	if err := settingsCollection.FindOne(ctx, bson.M{"project": syslogSettingsProject}).Decode(&doc); err != nil {
		return nil, err
	}
	if doc.SyslogConfig == nil {
		return nil, mongo.ErrNoDocuments
	}
	return doc.SyslogConfig, nil
}

// validateSyslogConfig enforces the closed-enum / range / required-field
// rules. Disabled configs only need basic shape (so operators can save a
// draft).
func validateSyslogConfig(cfg *models.SyslogConfig) error {
	if cfg.Protocol == "" {
		cfg.Protocol = syslog.ProtocolTCP
	}
	if !validProtocols[cfg.Protocol] {
		return fmt.Errorf("invalid protocol %q (allowed: udp, tcp, tcp+tls)", cfg.Protocol)
	}
	if !validFacilities[cfg.Facility] {
		return fmt.Errorf("invalid facility %q (allowed: local0..local7)", cfg.Facility)
	}

	if cfg.Enabled {
		if cfg.Host == "" {
			return fmt.Errorf("host is required when syslog is enabled")
		}
		if cfg.Port <= 0 || cfg.Port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
		// mTLS: a client cert without its key is invalid (and vice versa).
		if (cfg.ClientCert != "" && cfg.ClientKey == "") || (cfg.ClientCert == "" && cfg.ClientKey != "") {
			return fmt.Errorf("client_cert and client_key must be provided together")
		}
	}

	if cfg.QueueSize < 0 {
		return fmt.Errorf("queue_size must be ≥ 0")
	}
	if cfg.ConnectTimeoutMs < 0 || cfg.WriteTimeoutMs < 0 {
		return fmt.Errorf("timeouts must be ≥ 0")
	}

	// RFC5424 APP-NAME constrains Tag: 1..48 printable US-ASCII (no spaces,
	// brackets or control bytes). Reject obviously bad input rather than
	// rely on the formatter's silent sanitiser, so operators get feedback.
	if cfg.Tag != "" {
		if len(cfg.Tag) > 48 {
			return fmt.Errorf("tag must be ≤ 48 characters")
		}
		for i := 0; i < len(cfg.Tag); i++ {
			c := cfg.Tag[i]
			if c < 33 || c > 126 {
				return fmt.Errorf("tag must contain only printable US-ASCII (no spaces or control characters)")
			}
		}
	}
	return nil
}

// preserveSecretsOnEmpty applies LDAP-style preserve-on-empty: if the new
// payload omits a secret (either empty or sends the redact sentinel), the
// existing value is reused so the UI does not have to re-upload PEMs on
// every save.
func preserveSecretsOnEmpty(newCfg, existing *models.SyslogConfig) {
	if existing == nil {
		// First-time write — reject sentinels (cannot preserve what does not exist).
		if newCfg.CACert == redactSentinel {
			newCfg.CACert = ""
		}
		if newCfg.ClientCert == redactSentinel {
			newCfg.ClientCert = ""
		}
		if newCfg.ClientKey == redactSentinel {
			newCfg.ClientKey = ""
		}
		return
	}
	if newCfg.CACert == "" || newCfg.CACert == redactSentinel {
		newCfg.CACert = existing.CACert
	}
	if newCfg.ClientCert == "" || newCfg.ClientCert == redactSentinel {
		newCfg.ClientCert = existing.ClientCert
	}
	if newCfg.ClientKey == "" || newCfg.ClientKey == redactSentinel {
		newCfg.ClientKey = existing.ClientKey
	}
}

// maskSyslogConfigForResponse zeroes out PEM bodies and sets Has* booleans
// so GET callers learn presence without seeing content.
func maskSyslogConfigForResponse(cfg *models.SyslogConfig) {
	if cfg == nil {
		return
	}
	cfg.HasCACert = cfg.CACert != ""
	cfg.HasClientCert = cfg.ClientCert != ""
	cfg.HasClientKey = cfg.ClientKey != ""
	if cfg.HasCACert {
		cfg.CACert = redactSentinel
	}
	if cfg.HasClientCert {
		cfg.ClientCert = redactSentinel
	}
	if cfg.HasClientKey {
		cfg.ClientKey = redactSentinel
	}
}
