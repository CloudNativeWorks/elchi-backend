package handlers

import "github.com/gin-gonic/gin"

func (h *Handler) ListServices(c *gin.Context) {
	h.handleOpRequest(c, h.Service.ListServices)
}

func (h *Handler) GetService(c *gin.Context) {
	h.handleOpRequest(c, h.Service.GetService)
}

func (h *Handler) GetEnvoyDetails(c *gin.Context) {
	h.handleOpRequest(c, h.Service.GetEnvoyDetails)
}
