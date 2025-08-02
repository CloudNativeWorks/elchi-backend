package handlers

import (
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetScenarios(c *gin.Context) {
	h.handleRequest(c, h.Scenario.GetScenarios)
}

func (h *Handler) GetScenario(c *gin.Context) {
	h.handleRequest(c, h.Scenario.GetScenario)
}

func (h *Handler) SetScenario(c *gin.Context) {
	h.handleScenarioRequest(c, h.Scenario.SetScenario)
}
