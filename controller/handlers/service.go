package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) ListServices(c *gin.Context) {
	ctx := c.Request.Context()
	requestDetails, userDetails := h.getRequestDetails(c)

	// Add services-specific query parameters
	if requestDetails.Metadata == nil {
		requestDetails.Metadata = make(map[string]string)
	}

	// Get filtering parameters from query
	if name := c.Query("name"); name != "" {
		requestDetails.Metadata["name"] = name
	}
	if version := c.Query("version"); version != "" {
		requestDetails.Metadata["version"] = version
	}
	if downstream := c.Query("downstream_address"); downstream != "" {
		requestDetails.Metadata["downstream_address"] = downstream
	}
	if status := c.Query("status"); status != "" {
		requestDetails.Metadata["status"] = status
	}
	if page := c.Query("page"); page != "" {
		requestDetails.Metadata["page"] = page
	}
	if limit := c.Query("limit"); limit != "" {
		requestDetails.Metadata["limit"] = limit
	}

	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	response, err := h.dynamicOpFuncs(c, ctx, h.Service.ListServices, requestDetails)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": formatErrorMessage(err), "data": response})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": response})
}

func (h *Handler) GetService(c *gin.Context) {
	h.handleOpRequest(c, h.Service.GetService)
}

func (h *Handler) GetEnvoyDetails(c *gin.Context) {
	h.handleOpRequest(c, h.Service.GetEnvoyDetails)
}
