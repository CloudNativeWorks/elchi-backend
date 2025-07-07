package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (h *Handler) handleScenarioRequest(c *gin.Context, scFunc ScenarioFunc) {
	ctx := c.Request.Context()
	requestDetails, userDetails := h.getRequestDetails(c)

	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	response, err := h.dynamicScenarioFuncs(c, ctx, scFunc, requestDetails)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error(), "data": response})
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *Handler) dynamicScenarioFuncs(c *gin.Context, ctx context.Context, scFunc ScenarioFunc, requestDetails models.RequestDetails) (any, error) {
	resource, err := decodeScenarioResource(c)
	if err != nil {
		return nil, err
	}

	response, err := scFunc(ctx, *resource, requestDetails)
	if err != nil {
		return response, err
	}

	return response, nil
}

func decodeScenarioResource(c *gin.Context) (*models.ScenarioBody, error) {
	var body models.ScenarioBody
	if c.Request.Method != MethodGet && c.Request.Method != MethodDelete {
		err := c.BindJSON(&body)
		if err != nil {
			return nil, err
		}
	}
	return &body, nil
}
