package handler

import (
	"errors"

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
		state := service.RequestConversationExtractUnsupportedPayload
		switch {
		case errors.Is(err, securityaudit.ErrConversationInvalidJSON):
			state = service.RequestConversationExtractInvalidJSON
		case errors.Is(err, securityaudit.ErrNoConversationObservation):
			state = service.RequestConversationExtractNoNewUser
		case errors.Is(err, securityaudit.ErrConversationFilteredEmpty):
			state = service.RequestConversationExtractFilteredEmpty
		}
		service.BindRequestConversationExtractState(c, state)
		return
	}
	service.BindRequestConversationObservation(c, observation.CurrentUserText, observation.PreviousAssistantText)
}
