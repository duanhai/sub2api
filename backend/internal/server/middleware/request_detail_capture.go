package middleware

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type requestDetailBody struct {
	io.ReadCloser
	buf   bytes.Buffer
	limit int
}

func (b *requestDetailBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.buf.Len() < b.limit && n > 0 {
		remain := b.limit - b.buf.Len()
		if n < remain {
			remain = n
		}
		_, _ = b.buf.Write(p[:remain])
	}
	return n, err
}

// RequestDetailCapture observes gateway requests without eagerly reading or
// replacing their body, preserving compression and streaming request semantics.
func RequestDetailCapture(details *service.RequestDetailService) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		var body *requestDetailBody
		bodyLimit := 0
		if details != nil {
			bodyLimit = details.LiveBodyLimit()
		}
		if bodyLimit > 0 && c.Request.Body != nil && c.Request.Method != http.MethodGet {
			body = &requestDetailBody{ReadCloser: c.Request.Body, limit: bodyLimit}
			c.Request.Body = body
		}
		c.Next()
		if details == nil {
			return
		}
		key, authenticated := GetAPIKeyFromContext(c)
		if !authenticated || key == nil {
			return
		}
		item := service.RequestDetail{ID: c.GetHeader("X-Request-Id"), CreatedAt: started.UTC(), Method: c.Request.Method, Path: c.Request.URL.Path, StatusCode: c.Writer.Status(), DurationMs: time.Since(started).Milliseconds()}
		if item.ID == "" {
			item.ID = time.Now().UTC().Format("20060102T150405.000000000")
		}
		if subject, ok := GetAuthSubjectFromContext(c); ok {
			item.UserID = subject.UserID
		}
		item.APIKeyID = key.ID
		item.APIKeyName = key.Name
		if key.User != nil {
			item.UserEmail = key.User.Email
			item.Username = key.User.Username
		}
		item.GroupID = key.GroupID
		if body != nil && c.GetHeader("Content-Encoding") == "" && len(body.buf.Bytes()) > 0 {
			item.Model = service.RequestDetailModel(body.buf.Bytes())
			item.RequestBody = string(body.buf.Bytes())
		}
		details.Capture(c.Request.Context(), item)
	}
}
