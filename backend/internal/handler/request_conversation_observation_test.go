package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestBindResponsesConversationObservationStates(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		state string
	}{
		{name: "captured", body: `{"input":"new user request"}`, state: service.RequestConversationExtractCaptured},
		{name: "no new user", body: `{"input":[{"role":"user","content":"request"},{"role":"assistant","content":"response"},{"type":"function_call_output","output":"result"}]}`, state: service.RequestConversationExtractNoNewUser},
		{name: "filtered empty", body: `{"input":[{"role":"user","content":"# AGENTS.md instructions\n<INSTRUCTIONS>generated</INSTRUCTIONS>"}]}`, state: service.RequestConversationExtractFilteredEmpty},
		{name: "invalid JSON", body: `{`, state: service.RequestConversationExtractInvalidJSON},
		{name: "unsupported payload", body: `{"input":42}`, state: service.RequestConversationExtractUnsupportedPayload},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(nil)
			service.EnableRequestConversationCapture(c)
			bindResponsesConversationObservation(c, []byte(test.body))

			observation, ok := service.RequestConversationObservationFromContext(c)
			if !ok || observation.ExtractState != test.state {
				t.Fatalf("observation = %+v, want state %q", observation, test.state)
			}
		})
	}
}
