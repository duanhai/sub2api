package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetAPIKeyQueueSettings(c *gin.Context) {
	settings, err := h.settingService.GetAPIKeyQueueSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	configured := settings != nil
	if settings == nil {
		settings = &service.APIKeyQueueSettings{MaxWaiting: 6, TimeoutSeconds: 60}
	}
	response.Success(c, gin.H{"enabled": settings.Enabled, "max_waiting": settings.MaxWaiting, "timeout_seconds": settings.TimeoutSeconds, "configured": configured})
}

func (h *SettingHandler) UpdateAPIKeyQueueSettings(c *gin.Context) {
	var req struct {
		Enabled        *bool `json:"enabled"`
		MaxWaiting     *int  `json:"max_waiting"`
		TimeoutSeconds *int  `json:"timeout_seconds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil || req.MaxWaiting == nil || req.TimeoutSeconds == nil {
		response.BadRequest(c, "enabled, max_waiting and timeout_seconds are required")
		return
	}
	settings := service.APIKeyQueueSettings{Enabled: *req.Enabled, MaxWaiting: *req.MaxWaiting, TimeoutSeconds: *req.TimeoutSeconds}
	if err := settings.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.settingService.SetAPIKeyQueueSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": settings.Enabled, "max_waiting": settings.MaxWaiting, "timeout_seconds": settings.TimeoutSeconds, "configured": true})
}
