package service

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

func TestBindRequestConversationObservationUsesOneBoundedUTF8Budget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	EnableRequestConversationCapture(c)
	user := strings.Repeat("用", requestConversationTextLimitBytes/3)
	assistant := "assistant should be truncated"

	BindRequestConversationObservation(c, user, assistant)
	observation, ok := RequestConversationObservationFromContext(c)
	if !ok {
		t.Fatal("conversation observation was not bound")
	}
	if len(observation.CurrentUserText)+len(observation.PreviousAssistantText) > requestConversationTextLimitBytes {
		t.Fatalf("logged text exceeds budget: %+v", observation)
	}
	if !utf8.ValidString(observation.CurrentUserText) || !utf8.ValidString(observation.PreviousAssistantText) {
		t.Fatal("bounded observation contains invalid UTF-8")
	}
	if observation.TextState != RequestConversationTextTruncated {
		t.Fatalf("text state = %q, want truncated", observation.TextState)
	}
	if observation.ExtractState != RequestConversationExtractCaptured {
		t.Fatalf("extract state = %q, want captured", observation.ExtractState)
	}
}

func TestBindRequestConversationObservationRequiresCaptureFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	BindRequestConversationObservation(c, "user", "assistant")
	if _, ok := RequestConversationObservationFromContext(c); ok {
		t.Fatal("observation was bound while capture was disabled")
	}
}

func TestBindRequestConversationExtractState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	EnableRequestConversationCapture(c)
	BindRequestConversationExtractState(c, RequestConversationExtractNoNewUser)

	observation, ok := RequestConversationObservationFromContext(c)
	if !ok || observation.ExtractState != RequestConversationExtractNoNewUser {
		t.Fatalf("extract state was not bound: %+v", observation)
	}
	if observation.CurrentUserText != "" {
		t.Fatalf("failure state retained conversation text: %+v", observation)
	}
}
