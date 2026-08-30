package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func bindResponsesConversationObservation(c *gin.Context, body []byte) {
	if !service.RequestConversationCaptureEnabled(c) {
		return
	}
	observation, err := securityaudit.ExtractConversationObservation(securityaudit.Request{
		Protocol: service.ContentModerationProtocolOpenAIResponses,
		Body:     body,
	})
	if err != nil {
		return
	}
	service.BindRequestConversationObservation(c, observation.CurrentUserText, observation.PreviousAssistantText)
}
