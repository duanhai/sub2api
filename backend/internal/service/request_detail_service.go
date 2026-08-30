package service

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	requestDetailLogPathEnv        = "SUB2API_REQUEST_DETAIL_LOG_PATH"
	requestDetailLogBodyLimitKBEnv = "SUB2API_REQUEST_DETAIL_LOG_BODY_LIMIT_KB"
	requestDetailLogSourceEnv      = "SUB2API_REQUEST_DETAIL_LOG_SOURCE"
	requestDetailLogModeEnv        = "SUB2API_REQUEST_DETAIL_LOG_MODE"
	requestDetailLogQueueSize      = 32
	requestDetailDefaultBodyLimit  = 256 * 1024
)

const (
	requestDetailLogModeRaw        = "raw"
	requestDetailLogModeDual       = "dual"
	requestDetailLogModeStructured = "structured"
)

const (
	RequestBodyCaptured      = "captured"
	RequestBodyTruncated     = "truncated"
	RequestBodyNotApplicable = "not_applicable"
	RequestBodyEmpty         = "empty"
	RequestBodyDecodeFailed  = "decode_failed"
	RequestBodyStructured    = "structured"
)

type RequestDetail struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Model       string    `json:"model,omitempty"`
	StatusCode  int       `json:"status_code"`
	DurationMs  int64     `json:"duration_ms"`
	UserID      int64     `json:"user_id,omitempty"`
	APIKeyID    int64     `json:"api_key_id,omitempty"`
	APIKeyName  string    `json:"api_key_name,omitempty"`
	UserEmail   string    `json:"user_email,omitempty"`
	Username    string    `json:"username,omitempty"`
	GroupID     *int64    `json:"group_id,omitempty"`
	Source      string    `json:"source,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	LocalID     string    `json:"local_request_id,omitempty"`
	ClientID    string    `json:"client_request_id,omitempty"`
	UpstreamID  string    `json:"upstream_request_id,omitempty"`
	RequestSize int       `json:"request_size_bytes,omitempty"`
	RequestBody string    `json:"request_body,omitempty"`
	BodyState   string    `json:"body_state"`

	ConversationVersion    int    `json:"conversation_version,omitempty"`
	CurrentUserText        string `json:"current_user_text,omitempty"`
	PreviousAssistantText  string `json:"previous_assistant_text,omitempty"`
	CurrentUserBytes       int    `json:"current_user_size_bytes,omitempty"`
	PreviousAssistantBytes int    `json:"previous_assistant_size_bytes,omitempty"`
	ConversationTextState  string `json:"conversation_text_state,omitempty"`
	AssistantSource        string `json:"assistant_source,omitempty"`
	AssistantLag           int    `json:"assistant_lag,omitempty"`
}

type RequestDetailService struct {
	liveMu          sync.RWMutex
	liveSubscribers map[chan RequestDetail]int
	persistent      *requestDetailPersistentSink
}

func NewRequestDetailService() *RequestDetailService {
	return newRequestDetailService(newRequestDetailPersistentSinkFromEnv())
}

func newRequestDetailService(persistent *requestDetailPersistentSink) *RequestDetailService {
	return &RequestDetailService{
		liveSubscribers: make(map[chan RequestDetail]int),
		persistent:      persistent,
	}
}

func (s *RequestDetailService) LiveBodyLimit() int {
	if s == nil {
		return 0
	}
	s.liveMu.RLock()
	defer s.liveMu.RUnlock()
	limit := 0
	for _, candidate := range s.liveSubscribers {
		if candidate > limit {
			limit = candidate
		}
	}
	return limit
}

// CaptureBodyLimit is the largest body needed by either a live subscriber or
// the optional persistent log sink. A zero value keeps body capture disabled.
func (s *RequestDetailService) CaptureBodyLimit() int {
	if s == nil {
		return 0
	}
	limit := s.LiveBodyLimit()
	if s.persistent != nil && s.persistent.bodyLimit > limit {
		limit = s.persistent.bodyLimit
	}
	return limit
}

func (s *RequestDetailService) ConversationCaptureEnabled() bool {
	return s != nil && s.persistent != nil && s.persistent.mode != requestDetailLogModeRaw
}

func (s *RequestDetailService) SubscribeLive(bodyLimitKB int) (<-chan RequestDetail, func()) {
	if bodyLimitKB != 512 && bodyLimitKB != 1024 {
		bodyLimitKB = 256
	}
	ch := make(chan RequestDetail, 64)
	s.liveMu.Lock()
	s.liveSubscribers[ch] = bodyLimitKB * 1024
	s.liveMu.Unlock()
	return ch, func() {
		s.liveMu.Lock()
		if _, ok := s.liveSubscribers[ch]; ok {
			delete(s.liveSubscribers, ch)
			close(ch)
		}
		s.liveMu.Unlock()
	}
}

func (s *RequestDetailService) PublishLive(detail RequestDetail) {
	if s == nil {
		return
	}
	s.liveMu.RLock()
	defer s.liveMu.RUnlock()
	for ch, limit := range s.liveSubscribers {
		item := detail
		if len(item.RequestBody) > limit {
			item.RequestBody = item.RequestBody[:limit]
			item.BodyState = RequestBodyTruncated
		}
		select {
		case ch <- item:
		default:
		}
	}
}

// Publish fans out without waiting for disk or CLS collection. When the
// persistent queue is full, that copy is dropped while live delivery proceeds.
func (s *RequestDetailService) Publish(detail RequestDetail) {
	if s == nil {
		return
	}
	if s.persistent != nil {
		s.persistent.enqueue(detail)
	}
	s.PublishLive(detail)
}

func RequestDetailModel(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}

type requestDetailPersistentSink struct {
	bodyLimit   int
	source      string
	mode        string
	queue       chan RequestDetail
	writer      io.Writer
	dropped     atomic.Uint64
	writeErrors atomic.Uint64
}

func newRequestDetailPersistentSinkFromEnv() *requestDetailPersistentSink {
	path := strings.TrimSpace(os.Getenv(requestDetailLogPathEnv))
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil
	}
	writer := &lumberjack.Logger{
		Filename: path, MaxSize: 100, MaxBackups: 2, MaxAge: 1, Compress: true, LocalTime: true,
	}
	sink := newRequestDetailPersistentSink(
		writer,
		requestDetailBodyLimitFromEnv(os.Getenv(requestDetailLogBodyLimitKBEnv)),
		strings.TrimSpace(os.Getenv(requestDetailLogSourceEnv)),
		requestDetailLogQueueSize,
	)
	if sink != nil {
		sink.mode = requestDetailLogModeFromEnv(os.Getenv(requestDetailLogModeEnv))
	}
	return sink
}

func newRequestDetailPersistentSink(writer io.Writer, bodyLimit int, source string, queueSize int) *requestDetailPersistentSink {
	if writer == nil || bodyLimit <= 0 {
		return nil
	}
	if queueSize <= 0 {
		queueSize = requestDetailLogQueueSize
	}
	sink := &requestDetailPersistentSink{
		bodyLimit: bodyLimit,
		source:    strings.TrimSpace(source),
		mode:      requestDetailLogModeRaw,
		queue:     make(chan RequestDetail, queueSize),
		writer:    writer,
	}
	go sink.run()
	return sink
}

func requestDetailLogModeFromEnv(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case requestDetailLogModeDual:
		return requestDetailLogModeDual
	case requestDetailLogModeStructured:
		return requestDetailLogModeStructured
	default:
		return requestDetailLogModeRaw
	}
}

func requestDetailBodyLimitFromEnv(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return requestDetailDefaultBodyLimit
	}
	limitKB, err := strconv.Atoi(value)
	if err != nil || (limitKB != 256 && limitKB != 512) {
		return requestDetailDefaultBodyLimit
	}
	return limitKB * 1024
}

func (s *requestDetailPersistentSink) enqueue(detail RequestDetail) {
	if s == nil {
		return
	}
	detail.Source = s.source
	if s.mode == requestDetailLogModeStructured {
		detail.RequestBody = ""
		detail.BodyState = RequestBodyStructured
	}
	if len(detail.RequestBody) > s.bodyLimit {
		detail.RequestBody = detail.RequestBody[:s.bodyLimit]
		detail.BodyState = RequestBodyTruncated
	}
	select {
	case s.queue <- detail:
	default:
		s.dropped.Add(1)
	}
}

func (s *requestDetailPersistentSink) run() {
	encoder := json.NewEncoder(s.writer)
	encoder.SetEscapeHTML(false)
	for detail := range s.queue {
		if err := encoder.Encode(detail); err != nil {
			s.writeErrors.Add(1)
		}
	}
}
