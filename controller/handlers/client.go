package handlers

import "github.com/gin-gonic/gin"

func (h *Handler) ListClients(c *gin.Context) {
	h.handleOpRequest(c, h.Client.Handler.ListClients)
}

func (h *Handler) Commands(c *gin.Context) {
	h.handleOpRequest(c, h.Client.Handler.HandleSendCommand)
}

func (h *Handler) GetClient(c *gin.Context) {
	h.handleOpRequest(c, h.Client.Handler.GetClient)
}

func (h *Handler) DeleteClient(c *gin.Context) {
	h.handleOpRequest(c, h.Client.Handler.DeleteClient)
}
