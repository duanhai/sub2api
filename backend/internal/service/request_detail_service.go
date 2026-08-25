package service

import (
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const (
	RequestBodyCaptured      = "captured"
	RequestBodyTruncated     = "truncated"
	RequestBodyNotApplicable = "not_applicable"
	RequestBodyEmpty         = "empty"
	RequestBodyDecodeFailed  = "decode_failed"
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
	RequestBody string    `json:"request_body,omitempty"`
	BodyState   string    `json:"body_state"`
}

type RequestDetailService struct {
	liveMu          sync.RWMutex
	liveSubscribers map[chan RequestDetail]int
}

func NewRequestDetailService() *RequestDetailService {
	return &RequestDetailService{liveSubscribers: make(map[chan RequestDetail]int)}
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

func (s *RequestDetailService) SubscribeLive(bodyLimitKB int) (<-chan RequestDetail, func()) {
	if bodyLimitKB != 512 {
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

func RequestDetailModel(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}
