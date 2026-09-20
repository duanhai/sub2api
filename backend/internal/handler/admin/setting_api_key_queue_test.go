package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type apiKeyQueueSettingRepo struct{ settingHandlerRepoStub }

func (r *apiKeyQueueSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func TestAPIKeyQueuePanelSettingsRoundTrip(t *testing.T) {
	repo := &apiKeyQueueSettingRepo{settingHandlerRepoStub{values: map[string]string{}}}
	h := &SettingHandler{settingService: service.NewSettingService(repo, nil)}
	router := gin.New()
	router.GET("/queue", h.GetAPIKeyQueueSettings)
	router.PUT("/queue", h.UpdateAPIKeyQueueSettings)
	request := func(method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/queue", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		return rec
	}
	require.Contains(t, request(http.MethodGet, "").Body.String(), `"configured":false`)
	for _, body := range []string{`{}`, `{"enabled":true,"max_waiting":101,"timeout_seconds":20}`, `{"enabled":true,"max_waiting":2.5,"timeout_seconds":20}`} {
		require.Equal(t, http.StatusBadRequest, request(http.MethodPut, body).Code)
	}
	require.Empty(t, repo.values)
	rec := request(http.MethodPut, `{"enabled":true,"max_waiting":6,"timeout_seconds":60}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"configured":true`)
	rec = request(http.MethodGet, "")
	require.Contains(t, rec.Body.String(), `"enabled":true`)
	require.Contains(t, rec.Body.String(), `"max_waiting":6`)
	rec = request(http.MethodPut, `{"enabled":false,"max_waiting":6,"timeout_seconds":60}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, request(http.MethodGet, "").Body.String(), `"enabled":false`)
}
