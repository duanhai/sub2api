package middleware

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

type requestDetailBody struct {
	io.ReadCloser
	buf   bytes.Buffer
	limit int
	total int
}

func (b *requestDetailBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.total += n
	if b.buf.Len() < b.limit && n > 0 {
		remain := b.limit - b.buf.Len()
		if n < remain {
			remain = n
		}
		_, _ = b.buf.Write(p[:remain])
	}
	return n, err
}

func readLimitedBody(reader io.Reader, limit int) (string, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit+1)))
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
	}
	return string(data), truncated, nil
}

func decodeCapturedBody(raw []byte, encoding string, limit int) (string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return readLimitedBody(bytes.NewReader(raw), limit)
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", false, err
		}
		defer func() { _ = reader.Close() }()
		return readLimitedBody(reader, limit)
	case "deflate":
		reader, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", false, err
		}
		defer func() { _ = reader.Close() }()
		return readLimitedBody(reader, limit)
	case "zstd":
		reader, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", false, err
		}
		defer reader.Close()
		return readLimitedBody(reader, limit)
	default:
		return "", false, io.ErrUnexpectedEOF
	}
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
		captureActive := bodyLimit > 0
		originalEncoding := c.GetHeader("Content-Encoding")
		if captureActive && c.Request.Body != nil && c.Request.Method != http.MethodGet {
			body = &requestDetailBody{ReadCloser: c.Request.Body, limit: bodyLimit}
			c.Request.Body = body
		}
		c.Next()
		if details == nil || !captureActive {
			return
		}
		key, authenticated := GetAPIKeyFromContext(c)
		if !authenticated || key == nil {
			return
		}
		item := service.RequestDetail{ID: c.GetHeader("X-Request-Id"), CreatedAt: started.UTC(), Method: c.Request.Method, Path: c.Request.URL.Path, StatusCode: c.Writer.Status(), DurationMs: time.Since(started).Milliseconds(), BodyState: service.RequestBodyNotApplicable}
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
		if c.Request.Method != http.MethodGet {
			item.BodyState = service.RequestBodyEmpty
			if body != nil && body.total > 0 {
				if body.total > body.limit && strings.TrimSpace(originalEncoding) != "" {
					item.BodyState = service.RequestBodyTruncated
				} else if decoded, truncated, err := decodeCapturedBody(body.buf.Bytes(), originalEncoding, body.limit); err != nil {
					item.BodyState = service.RequestBodyDecodeFailed
				} else {
					item.RequestBody = decoded
					item.Model = service.RequestDetailModel([]byte(decoded))
					if truncated || body.total > body.limit {
						item.BodyState = service.RequestBodyTruncated
					} else {
						item.BodyState = service.RequestBodyCaptured
					}
				}
			}
		}
		details.PublishLive(item)
	}
}
