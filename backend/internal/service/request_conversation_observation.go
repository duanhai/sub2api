package service

import (
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	requestConversationCaptureContextKey     = "sub2api.request_conversation.capture"
	requestConversationObservationContextKey = "sub2api.request_conversation.observation"
	requestConversationTextLimitBytes        = 64 * 1024
)

const (
	RequestConversationTextCaptured  = "captured"
	RequestConversationTextTruncated = "truncated"
)

type RequestConversationObservation struct {
	CurrentUserText        string
	PreviousAssistantText  string
	CurrentUserBytes       int
	PreviousAssistantBytes int
	TextState              string
}

func EnableRequestConversationCapture(c *gin.Context) {
	if c != nil {
		c.Set(requestConversationCaptureContextKey, true)
	}
}

func RequestConversationCaptureEnabled(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, exists := c.Get(requestConversationCaptureContextKey)
	enabled, _ := value.(bool)
	return exists && enabled
}

func BindRequestConversationObservation(c *gin.Context, currentUserText, previousAssistantText string) {
	if c == nil || !RequestConversationCaptureEnabled(c) {
		return
	}
	currentUserText = strings.TrimSpace(currentUserText)
	previousAssistantText = strings.TrimSpace(previousAssistantText)
	if currentUserText == "" {
		return
	}

	observation := RequestConversationObservation{
		CurrentUserBytes:       len(currentUserText),
		PreviousAssistantBytes: len(previousAssistantText),
		TextState:              RequestConversationTextCaptured,
	}
	remaining := requestConversationTextLimitBytes
	observation.CurrentUserText, remaining = trimConversationText(currentUserText, remaining)
	observation.PreviousAssistantText, remaining = trimConversationText(previousAssistantText, remaining)
	if len(observation.CurrentUserText) < observation.CurrentUserBytes ||
		len(observation.PreviousAssistantText) < observation.PreviousAssistantBytes {
		observation.TextState = RequestConversationTextTruncated
	}
	c.Set(requestConversationObservationContextKey, observation)
}

func RequestConversationObservationFromContext(c *gin.Context) (RequestConversationObservation, bool) {
	if c == nil {
		return RequestConversationObservation{}, false
	}
	value, exists := c.Get(requestConversationObservationContextKey)
	observation, ok := value.(RequestConversationObservation)
	return observation, exists && ok
}

func trimConversationText(value string, limit int) (string, int) {
	if limit <= 0 || value == "" {
		return "", max(limit, 0)
	}
	if len(value) <= limit {
		return value, limit - len(value)
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], limit - end
}
