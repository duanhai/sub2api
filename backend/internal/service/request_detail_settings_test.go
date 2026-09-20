package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type requestDetailSettingsRepo struct {
	SettingRepository
	mu  sync.Mutex
	raw string
	err error
}

func (r *requestDetailSettingsRepo) GetValue(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.raw, r.err
}

func (r *requestDetailSettingsRepo) Set(_ context.Context, _, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.raw = value
	return nil
}

func detailSettingsService(t *testing.T, repo SettingRepository) *RequestDetailService {
	t.Helper()
	svc := ProvideRequestDetailService(repo)
	t.Cleanup(svc.Stop)
	return svc
}

func TestRequestDetailSettingsDefaultAndLegacyMigration(t *testing.T) {
	t.Setenv(requestDetailLogPathEnv, "")
	t.Setenv(requestDetailLogModeEnv, "")
	t.Setenv(requestDetailLogSourceEnv, "")
	t.Setenv(requestDetailLogBodyLimitKBEnv, "")
	repo := &requestDetailSettingsRepo{}
	svc := detailSettingsService(t, repo)
	view := svc.GetLogSettings()
	require.False(t, view.Enabled)
	require.False(t, view.Active)
	require.False(t, view.Configured)
	require.Equal(t, defaultRequestDetailLogPath, view.Path)
	require.Zero(t, svc.CaptureBodyLimit())

	legacyPath := filepath.Join(t.TempDir(), "legacy", "requests.jsonl")
	t.Setenv(requestDetailLogPathEnv, legacyPath)
	t.Setenv(requestDetailLogModeEnv, "dual")
	t.Setenv(requestDetailLogSourceEnv, "old.example")
	t.Setenv(requestDetailLogBodyLimitKBEnv, "512")
	legacy := detailSettingsService(t, repo)
	view = legacy.GetLogSettings()
	require.True(t, view.Active)
	require.Equal(t, "dual", view.Mode)
	require.Equal(t, 512, view.BodyLimitKB)
	require.Equal(t, legacyPath, view.Path)
	view, err := legacy.UpdateLogSettings(context.Background(), view.RequestDetailLogSettings)
	require.NoError(t, err)
	require.True(t, view.Configured)
	legacy.Stop()

	// Removing legacy environment policy does not lose the saved policy/path.
	t.Setenv(requestDetailLogPathEnv, "")
	t.Setenv(requestDetailLogModeEnv, "raw")
	restarted := detailSettingsService(t, repo)
	require.Equal(t, legacyPath, restarted.GetLogSettings().Path)
	require.Equal(t, "dual", restarted.GetLogSettings().Mode)
	require.Equal(t, "old.example", restarted.GetLogSettings().Source)
	restarted.Stop()

	// Explicit host overrides still allow relocation to a different mount.
	newPath := filepath.Join(t.TempDir(), "moved.jsonl")
	t.Setenv(requestDetailLogPathEnv, newPath)
	moved := detailSettingsService(t, repo)
	require.Equal(t, newPath, moved.GetLogSettings().Path)
}

func TestRequestDetailSettingsHotUpdateDrainsAndKeepsLiveViewing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv(requestDetailLogPathEnv, path)
	t.Setenv(requestDetailLogModeEnv, "raw")
	repo := &requestDetailSettingsRepo{}
	svc := detailSettingsService(t, repo)
	originalSink := svc.persistent
	cfg := svc.GetLogSettings().RequestDetailLogSettings
	cfg.Mode, cfg.Source, cfg.BodyLimitKB = "structured", "panel", 512
	_, err := svc.UpdateLogSettings(context.Background(), cfg)
	require.NoError(t, err)
	require.True(t, svc.ConversationCaptureEnabled())
	require.Equal(t, 512*1024, svc.CaptureBodyLimit())
	svc.Publish(RequestDetail{ID: "recorded", Path: "/responses", StatusCode: 200, RequestBody: "raw",
		ConversationExtractState: RequestConversationExtractCaptured, CurrentUserText: "hello"})

	cfg.Enabled = false
	_, err = svc.UpdateLogSettings(context.Background(), cfg)
	require.NoError(t, err)
	require.Zero(t, svc.CaptureBodyLimit())
	require.False(t, svc.ConversationCaptureEnabled())
	events, unsubscribe := svc.SubscribeLive(256)
	defer unsubscribe()
	svc.Publish(RequestDetail{ID: "live-only"})
	require.Equal(t, "live-only", (<-events).ID)
	require.Equal(t, 256*1024, svc.CaptureBodyLimit())

	cfg.Enabled, cfg.Mode = true, "raw"
	_, err = svc.UpdateLogSettings(context.Background(), cfg)
	require.NoError(t, err)
	require.Same(t, originalSink, svc.persistent, "hot updates reuse one writer")
	svc.Publish(RequestDetail{ID: "resumed", RequestBody: "raw"})
	svc.Stop() // waits for queued records, including the pre-disable record
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	var first, second RequestDetail
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	require.Equal(t, "panel", first.Source)
	require.Empty(t, first.RequestBody)
	require.Equal(t, "hello", first.CurrentUserText)
	require.Equal(t, "resumed", second.ID)
	require.Equal(t, "raw", second.RequestBody)
}

func TestRequestDetailSettingsFailuresRetainPolicy(t *testing.T) {
	t.Setenv(requestDetailLogPathEnv, filepath.Join(t.TempDir(), "requests.jsonl"))
	repo := &requestDetailSettingsRepo{}
	svc := detailSettingsService(t, repo)
	before := svc.GetLogSettings()
	for _, invalid := range []RequestDetailLogSettings{
		{Mode: "unknown", BodyLimitKB: 256},
		{Mode: "raw", BodyLimitKB: 1024},
		{Mode: "raw", BodyLimitKB: 256, Source: strings.Repeat("x", 257)},
	} {
		_, err := svc.UpdateLogSettings(context.Background(), invalid)
		require.Error(t, err)
	}
	require.Empty(t, repo.raw)
	repo.mu.Lock()
	repo.err = errors.New("database down")
	repo.mu.Unlock()
	off := before.RequestDetailLogSettings
	off.Enabled = false
	_, err := svc.UpdateLogSettings(context.Background(), off)
	require.Error(t, err)
	require.Error(t, svc.syncLogSettings(context.Background()))
	require.Equal(t, before.RequestDetailLogSettings, svc.GetLogSettings().RequestDetailLogSettings)
	require.True(t, svc.GetLogSettings().Active)
	require.NotEmpty(t, svc.GetLogSettings().RuntimeError)
}

func TestRequestDetailSettingsStartupReadFailureRecoversSavedPath(t *testing.T) {
	t.Setenv(requestDetailLogPathEnv, "")
	path := filepath.Join(t.TempDir(), "saved.jsonl")
	cfg := RequestDetailLogSettings{Enabled: true, Mode: "dual", BodyLimitKB: 256, Path: path}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &requestDetailSettingsRepo{raw: string(raw), err: errors.New("database down")}
	svc := detailSettingsService(t, repo)
	require.False(t, svc.GetLogSettings().Active)
	require.NotEmpty(t, svc.GetLogSettings().RuntimeError)
	_, err = svc.UpdateLogSettings(context.Background(), cfg)
	require.Error(t, err, "must not overwrite unread settings")
	repo.mu.Lock()
	repo.err = nil
	repo.mu.Unlock()
	require.NoError(t, svc.syncLogSettings(context.Background()))
	require.Equal(t, path, svc.GetLogSettings().Path)
	require.True(t, svc.GetLogSettings().Active)
	require.Empty(t, svc.GetLogSettings().RuntimeError)
}

func TestRequestDetailSettingsBadPathDoesNotStopGateway(t *testing.T) {
	// A directory cannot be opened as a JSONL file on Windows or Linux.
	t.Setenv(requestDetailLogPathEnv, t.TempDir())
	repo := &requestDetailSettingsRepo{}
	svc := detailSettingsService(t, repo)
	view := svc.GetLogSettings()
	require.True(t, view.Enabled)
	require.False(t, view.Active)
	require.NotEmpty(t, view.RuntimeError)
	_, err := svc.UpdateLogSettings(context.Background(), view.RequestDetailLogSettings)
	require.Error(t, err)
	require.Empty(t, repo.raw)
	view.Enabled = false
	updated, err := svc.UpdateLogSettings(context.Background(), view.RequestDetailLogSettings)
	require.NoError(t, err)
	require.Empty(t, updated.RuntimeError)
}

func TestRequestDetailSettingsEnableFailureDoesNotInstallWriter(t *testing.T) {
	t.Setenv(requestDetailLogPathEnv, "")
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	cfg := RequestDetailLogSettings{Mode: "raw", BodyLimitKB: 256, Path: path}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &requestDetailSettingsRepo{raw: string(raw)}
	svc := detailSettingsService(t, repo)
	repo.mu.Lock()
	repo.err = errors.New("write failed")
	repo.mu.Unlock()
	cfg.Enabled = true
	_, err = svc.UpdateLogSettings(context.Background(), cfg)
	require.Error(t, err)
	require.False(t, svc.GetLogSettings().Active)
	require.Nil(t, svc.persistent)
	require.NoError(t, os.Remove(path), "failed save must release the prepared file")
	repo.mu.Lock()
	repo.err = nil
	repo.mu.Unlock()
	view, err := svc.UpdateLogSettings(context.Background(), cfg)
	require.NoError(t, err)
	require.True(t, view.Active)
}

func TestRequestDetailSettingsSyncAndConcurrentPublish(t *testing.T) {
	t.Setenv(requestDetailLogPathEnv, filepath.Join(t.TempDir(), "requests.jsonl"))
	repo := &requestDetailSettingsRepo{}
	svc := detailSettingsService(t, repo)
	cfg := svc.GetLogSettings().RequestDetailLogSettings
	cfg.Enabled = false
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, repo.Set(context.Background(), requestDetailSettingsKey, string(raw)))
	require.NoError(t, svc.syncLogSettings(context.Background()))
	require.False(t, svc.GetLogSettings().Active)

	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				svc.CaptureBodyLimit()
				svc.ConversationCaptureEnabled()
				svc.GetLogSettings()
				svc.Publish(RequestDetail{ID: "concurrent", RequestBody: "body"})
			}
		}()
	}
	for i := 0; i < 20; i++ {
		cfg.Enabled = i%2 == 0
		cfg.Source = "updated"
		_, err := svc.UpdateLogSettings(context.Background(), cfg)
		require.NoError(t, err)
	}
	workers.Wait()
}
