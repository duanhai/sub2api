package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpsHandler) GetRequestDetailLogSettings(c *gin.Context) {
	if h.requestDetails == nil {
		response.Error(c, http.StatusServiceUnavailable, "Request detail service not available")
		return
	}
	response.Success(c, h.requestDetails.GetLogSettings())
}

func (h *OpsHandler) UpdateRequestDetailLogSettings(c *gin.Context) {
	if h.requestDetails == nil {
		response.Error(c, http.StatusServiceUnavailable, "Request detail service not available")
		return
	}
	var req struct {
		Enabled     *bool   `json:"enabled"`
		Mode        *string `json:"mode"`
		BodyLimitKB *int    `json:"body_limit_kb"`
		Source      *string `json:"source"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil || req.Mode == nil || req.BodyLimitKB == nil || req.Source == nil {
		response.BadRequest(c, "enabled, mode, body_limit_kb and source are required")
		return
	}
	cfg := service.RequestDetailLogSettings{Enabled: *req.Enabled, Mode: *req.Mode, BodyLimitKB: *req.BodyLimitKB, Source: *req.Source}
	if err := cfg.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	updated, err := h.requestDetails.UpdateLogSettings(c.Request.Context(), cfg)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Unable to save request detail settings; check database and log directory permissions")
		return
	}
	response.Success(c, updated)
}
