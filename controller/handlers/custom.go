package handlers

import (
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetCustomResourceList(c *gin.Context) {
	h.handleRequest(c, h.Custom.GetCustomResourceList)
}

func (h *Handler) GetCustomHTTPFilterList(c *gin.Context) {
	h.handleRequest(c, h.Custom.GetCustomHTTPFilterList)
}

func (h *Handler) GetAvailableVersions(c *gin.Context) {
	h.handleRequest(c, h.Custom.GetAvailableVersions)
}

func (h *Handler) GetFilterCounts(c *gin.Context) {
	h.handleRequest(c, h.Custom.GetFilterCounts)
}

func (h *Handler) GetResourceCounts(c *gin.Context) {
	h.handleRequest(c, h.Custom.GetResourceCounts)
}

func (h *Handler) GetErrorSummary(c *gin.Context) {
	h.handleRequest(c, h.Custom.GetErrorSummary)
}
