package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetTopicSummarySettings(c *gin.Context) {
	if h == nil || h.topicSummaryService == nil {
		response.InternalError(c, "Topic summary service is unavailable")
		return
	}
	settings, err := h.topicSummaryService.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *SettingHandler) UpdateTopicSummarySettings(c *gin.Context) {
	if h == nil || h.topicSummaryService == nil {
		response.InternalError(c, "Topic summary service is unavailable")
		return
	}
	var req securityaudit.UpdateTopicSummarySettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings, err := h.topicSummaryService.UpdateSettings(c.Request.Context(), req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, settings)
}
