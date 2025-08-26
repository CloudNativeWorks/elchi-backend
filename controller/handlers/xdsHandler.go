package handlers

import (
	"github.com/gin-gonic/gin"
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

func (h *Handler) UpdateResource(c *gin.Context) {
	h.handleRequest(c, h.XDS.UpdateResource)
}
