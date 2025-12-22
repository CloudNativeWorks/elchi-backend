package handlers

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/gin-gonic/gin"
)

// ACMEAuditHelper handles audit context for ACME operations
type ACMEAuditHelper struct{}

// NewACMEAuditHelper creates a new ACME audit helper
func NewACMEAuditHelper() *ACMEAuditHelper {
	return &ACMEAuditHelper{}
}

// SetDNSCredentialAuditContext sets audit context for DNS credential operations
func (h *ACMEAuditHelper) SetDNSCredentialAuditContext(c *gin.Context, action, credID, credName, project string) {
	if action == "" {
		return // Skip for GET requests
	}
	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, "acme_dns_credential", credID, credName, project)
}

// SetACMEAccountAuditContext sets audit context for ACME account operations
func (h *ACMEAuditHelper) SetACMEAccountAuditContext(c *gin.Context, action, accountID, accountName, project string) {
	if action == "" {
		return
	}
	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, "acme_account", accountID, accountName, project)
}

// SetCertificateAuditContext sets audit context for certificate operations
func (h *ACMEAuditHelper) SetCertificateAuditContext(c *gin.Context, action, certID, certName, project string) {
	if action == "" {
		return
	}
	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, "acme_certificate", certID, certName, project)
}

// SetAuditError sets error message in audit context
func (h *ACMEAuditHelper) SetAuditError(c *gin.Context, err error) {
	if err != nil {
		audit.SetAuditError(c, err.Error())
	}
}

// SetAuditSuccess sets success flag in audit context
func (h *ACMEAuditHelper) SetAuditSuccess(c *gin.Context) {
	audit.SetAuditSuccess(c, true)
}
