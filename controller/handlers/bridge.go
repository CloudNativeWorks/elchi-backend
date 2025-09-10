package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetNodeSnapshot(c *gin.Context) {
	// Set audit context for snapshot operations
	h.setBridgeAuditContext(c)

	nodeID := c.Param("nodeID")
	version := c.Query("version") // Optional Envoy version for routing

	response, err := h.Bridge.GetNodeSnapshot(c.Request.Context(), nodeID, version)

	// Set audit result
	h.setAuditResult(c, err)

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Success",
		"data":    response,
	})
}

func (h *Handler) ClearNodeSnapshot(c *gin.Context) {
	// Set audit context for snapshot operations
	h.setBridgeAuditContext(c)

	nodeID := c.Param("nodeID")

	response, err := h.Bridge.ClearNodeSnapshot(c.Request.Context(), nodeID)

	// Set audit result
	h.setAuditResult(c, err)

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Snapshot cleared successfully",
		"data":    response,
	})
}
