package handlers

import (
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetResourceDependencies(c *gin.Context) {
	h.handleDepRequest(c, h.dependency.GetResourceDependencies)
}
