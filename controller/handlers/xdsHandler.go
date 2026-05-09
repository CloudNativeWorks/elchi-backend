package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security/secretredact"
)

func (h *Handler) SetResource(c *gin.Context) {
	h.handleRequest(c, h.XDS.SetResource)
}

func (h *Handler) GetResource(c *gin.Context) {
	h.handleRequest(c, h.XDS.GetResource)
}

func (h *Handler) ListResource(c *gin.Context) {
	// Always use pagination endpoint
	h.handlePaginatedRequest(c, h.XDS.ListResourceWithPagination)
}

func (h *Handler) DelResource(c *gin.Context) {
	h.handleRequest(c, h.XDS.DelResource)
}

// UpdateResource is the PUT entry point for XDS resources.
//
// For the secrets collection it must rewrite the request body BEFORE
// handleRequest's audit + validation pipeline runs. The user only ever
// sees ***REDACTED*** in `inline_string` / `inline_bytes` leaves; if the
// FE sends back a body containing those sentinels the original values
// must be merged in from the existing DB document, otherwise:
//
//   - validation.GlobalValidatorRegistry.ValidateByGType (cert.go) would
//     reject the sentinel as invalid PEM,
//   - resources.ValidateResourceWithClient would reject it at the
//     control-plane gRPC layer,
//   - and the user's no-op PUT would silently wipe their TLS material.
//
// The merge happens up here so it predates setAuditChanges (which reads
// _original_body for the diff) and decodeR (which also reads
// _original_body to bind the typed struct). Overriding _original_body
// flows through both.
func (h *Handler) UpdateResource(c *gin.Context) {
	requestDetails, userDetails := h.getRequestDetails(c)

	if requestDetails.Collection == xds.SecretsCollection && c.Request.Method == http.MethodPut {
		// Auth must run BEFORE the merge fetch — otherwise an unauthorized
		// caller could probe MongoDB by varying (name, version, project)
		// and time the response (existence oracle). We mirror the same
		// gates handleRequest will apply later: role check + project
		// access. The fetch itself also uses AddSecureUserFilter so even
		// if the caller has SOME project access, they cannot read secrets
		// outside their group/permission scope.
		if err := checkRole(c, userDetails); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		if requestDetails.Project != "" {
			if db := h.getDatabaseConnection(); db != nil {
				if err := authorization.ValidateRequestProject(c.Request.Context(), db, userDetails, requestDetails.Project); err != nil {
					c.JSON(http.StatusForbidden, gin.H{"message": err.Error()})
					return
				}
			}
		}

		if err := h.applySecretRedactionMerge(c, requestDetails); err != nil {
			// Generic message to the caller; details only on the server log.
			// Avoids leaking Mongo internals (collection names, command
			// errors) into 4xx response bodies.
			if h.XDS != nil && h.XDS.Logger != nil {
				h.XDS.Logger.Errorf("secret redaction merge failed for %s/%s: %v",
					requestDetails.Project, requestDetails.Name, err)
			}
			c.JSON(http.StatusBadRequest, gin.H{"message": "invalid secret update request"})
			return
		}
	}

	h.handleRequest(c, h.XDS.UpdateResource)
}

// applySecretRedactionMerge restores any redaction sentinels in the
// incoming request body from the matching MongoDB document and rewrites
// `_original_body` (and c.Request.Body) so the rest of the request
// pipeline sees the merged bytes.
//
// Failures at the merge stage are not silently swallowed — a missing
// existing document, a malformed body, or a Mongo error would surface as
// 400 to the caller. That keeps the audit / validation contract intact:
// either the merge succeeded and downstream sees real values, or the
// request is rejected before any state mutation.
func (h *Handler) applySecretRedactionMerge(c *gin.Context, requestDetails models.RequestDetails) error {
	originalAny, exists := c.Get("_original_body")
	if !exists {
		return errors.New("_original_body missing — body capture middleware not run?")
	}
	bodyBytes, ok := originalAny.([]byte)
	if !ok {
		return errors.New("_original_body has unexpected type")
	}
	if len(bodyBytes) == 0 {
		// PUT with empty body — let the normal pipeline produce its own
		// "invalid request" error.
		return nil
	}

	db := h.getDatabaseConnection()
	if db == nil {
		return errors.New("database connection unavailable")
	}

	existingJSON, err := fetchExistingSecretJSON(c.Request.Context(), db, requestDetails)
	if err != nil {
		// If the existing doc cannot be read (not found, auth, etc.) we
		// can't safely merge, but we also shouldn't 500 — let the normal
		// update path produce its own error.
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil
		}
		return fmt.Errorf("fetch existing secret: %w", err)
	}

	merged, err := secretredact.MergeJSON(bodyBytes, existingJSON)
	if err != nil {
		return fmt.Errorf("merge sentinels: %w", err)
	}

	// Override the cached body so setAuditChanges and decodeR both see
	// the merged version. Replace c.Request.Body too as a belt-and-braces
	// guard in case any downstream code re-reads it.
	c.Set("_original_body", merged)
	c.Request.Body = io.NopCloser(bytes.NewReader(merged))
	c.Request.ContentLength = int64(len(merged))

	return nil
}

// fetchExistingSecretJSON loads the existing secret document from
// MongoDB using the same identity tuple update_xds.go uses
// (general.name + general.version + general.project), gated by
// AddSecureUserFilter so the caller's group/permission scope is honored
// (mirroring the visibility update_xds.go itself enforces). Any
// decode/marshal error bubbles up.
func fetchExistingSecretJSON(ctx context.Context, db *mongo.Database, requestDetails models.RequestDetails) ([]byte, error) {
	baseFilter := bson.M{
		"general.name":    requestDetails.Name,
		"general.version": requestDetails.Version,
		"general.project": requestDetails.Project,
	}
	filter, err := common.AddSecureUserFilter(ctx, db, requestDetails, baseFilter)
	if err != nil {
		return nil, fmt.Errorf("apply security filter: %w", err)
	}
	var existing models.DBResource
	if err := db.Collection(xds.SecretsCollection).FindOne(ctx, filter).Decode(&existing); err != nil {
		return nil, err
	}
	return json.Marshal(&existing)
}
