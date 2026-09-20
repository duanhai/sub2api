package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type requestDetailPanelRepo struct {
	service.SettingRepository
	mu  sync.Mutex
	raw string
}

func (r *requestDetailPanelRepo) GetValue(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.raw, nil
}

func (r *requestDetailPanelRepo) Set(_ context.Context, _, raw string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.raw = raw
	return nil
}

func TestRequestDetailPanelValidationAndReadOnlyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("SUB2API_REQUEST_DETAIL_LOG_PATH", path)
	svc := service.ProvideRequestDetailService(&requestDetailPanelRepo{})
	t.Cleanup(svc.Stop)
	h := NewOpsHandler(nil, svc)
	router := gin.New()
	router.GET("/settings", h.GetRequestDetailLogSettings)
	router.PUT("/settings", h.UpdateRequestDetailLogSettings)
	request := func(method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, body := range []string{
		`{}`, `{"enabled":false,"mode":"raw","body_limit_kb":256}`,
		`{"enabled":true,"mode":"bad","body_limit_kb":256,"source":""}`,
		`{"enabled":true,"mode":"raw","body_limit_kb":256.5,"source":""}`,
	} {
		require.Equal(t, http.StatusBadRequest, request(http.MethodPut, body).Code)
	}
	rec := request(http.MethodPut, `{"enabled":false,"mode":"dual","body_limit_kb":512,"source":"panel","path":"/arbitrary/file"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, path, svc.GetLogSettings().Path)
	require.False(t, svc.GetLogSettings().Active)
	require.Contains(t, request(http.MethodGet, "").Body.String(), `"configured":true`)
}
