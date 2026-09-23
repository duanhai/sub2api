package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const requestDetailSettingsKey = "request_detail_logging"
const defaultRequestDetailLogPath = "/app/data/request-details/request-details.jsonl"

// Path is persisted to retain legacy collection locations when environment
// settings are removed. It is read-only in the admin API. An explicit local
// environment path still takes precedence when moving to another host.
type RequestDetailLogSettings struct {
	Enabled     bool   `json:"enabled"`
	Mode        string `json:"mode"`
	BodyLimitKB int    `json:"body_limit_kb"`
	Source      string `json:"source"`
	Path        string `json:"path"`
}

func (s RequestDetailLogSettings) Validate() error {
	if s.Mode != requestDetailLogModeRaw && s.Mode != requestDetailLogModeDual && s.Mode != requestDetailLogModeStructured {
		return errors.New("mode must be raw, dual or structured")
	}
	if s.BodyLimitKB != 256 && s.BodyLimitKB != 512 {
		return errors.New("body_limit_kb must be 256 or 512")
	}
	if len(s.Source) > 256 || strings.ContainsAny(s.Source, "\r\n\x00") {
		return errors.New("source must be at most 256 bytes without line breaks")
	}
	return nil
}

type RequestDetailLogSettingsView struct {
	RequestDetailLogSettings
	Configured   bool   `json:"configured"`
	Active       bool   `json:"active"`
	RuntimeError string `json:"runtime_error,omitempty"`
	Dropped      uint64 `json:"dropped"`
	WriteErrors  uint64 `json:"write_errors"`
}

func requestDetailSettingsFromEnv() RequestDetailLogSettings {
	path := strings.TrimSpace(os.Getenv(requestDetailLogPathEnv))
	return RequestDetailLogSettings{
		Enabled: path != "", Path: path,
		Mode:        requestDetailLogModeFromEnv(os.Getenv(requestDetailLogModeEnv)),
		BodyLimitKB: requestDetailBodyLimitFromEnv(os.Getenv(requestDetailLogBodyLimitKBEnv)) / 1024,
		Source:      strings.TrimSpace(os.Getenv(requestDetailLogSourceEnv)),
	}
}

func readRequestDetailSettings(ctx context.Context, repo SettingRepository) (*RequestDetailLogSettings, error) {
	raw, err := repo.GetValue(ctx, requestDetailSettingsKey)
	if errors.Is(err, ErrSettingNotFound) || (err == nil && raw == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg RequestDetailLogSettings
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}
	return &cfg, cfg.Validate()
}

func ProvideRequestDetailService(repo SettingRepository) *RequestDetailService {
	s := newRequestDetailService(nil)
	s.settingRepo = repo
	s.logSettings = requestDetailSettingsFromEnv()
	if s.logSettings.Path == "" {
		s.logSettings.Path = defaultRequestDetailLogPath
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := s.syncLogSettings(ctx); err != nil {
		// Optional logging must not prevent the gateway from starting. Surface
		// the failure in the panel and retry without claiming recording is active.
		log.Printf("request detail logging: %v", err)
	}
	cancel()
	s.stop, s.stopped = make(chan struct{}), make(chan struct{})
	go s.refreshLogSettings()
	return s
}

func (s *RequestDetailService) GetLogSettings() RequestDetailLogSettingsView {
	s.persistentMu.RLock()
	defer s.persistentMu.RUnlock()
	view := RequestDetailLogSettingsView{
		RequestDetailLogSettings: s.logSettings, Configured: s.configured,
		Active: s.recording, RuntimeError: s.runtimeError,
	}
	if s.persistent != nil {
		view.Dropped = s.persistent.dropped.Load()
		view.WriteErrors = s.persistent.writeErrors.Load()
	}
	return view
}

// prepareLogSink runs filesystem work off the gateway lock. A service keeps one
// writer for its fixed path, including while recording is disabled.
func (s *RequestDetailService) prepareLogSink(cfg RequestDetailLogSettings) (*requestDetailPersistentSink, error) {
	if !cfg.Enabled || s.persistent != nil {
		return s.persistent, nil
	}
	path := filepath.Clean(cfg.Path)
	if !filepath.IsAbs(path) {
		return nil, errors.New("request detail log path must be absolute")
	}
	//nolint:gosec // Administrator-controlled startup path, cleaned and required to be absolute.
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create request detail log directory: %w", err)
	}
	// Check writability before acknowledging a settings save. Lumberjack opens
	// lazily, so constructing it alone would hide permission failures.
	//nolint:gosec // Administrator-controlled startup path, cleaned and required to be absolute; not editable through the API.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open request detail log: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	writer := &lumberjack.Logger{Filename: path, MaxSize: 100, MaxBackups: 2, MaxAge: 1, Compress: true, LocalTime: true}
	return newRequestDetailPersistentSink(writer, cfg.BodyLimitKB*1024, cfg.Source, requestDetailLogQueueSize), nil
}

func (s *RequestDetailService) installLogSettings(cfg RequestDetailLogSettings, sink *requestDetailPersistentSink, configured bool) {
	s.persistentMu.Lock()
	defer s.persistentMu.Unlock()
	s.persistent = sink
	if sink != nil {
		sink.mode, sink.source, sink.bodyLimit = cfg.Mode, cfg.Source, cfg.BodyLimitKB*1024
	}
	s.logSettings, s.configured, s.recording, s.runtimeError = cfg, configured, cfg.Enabled && sink != nil, ""
}

func (s *RequestDetailService) applyLogSettings(cfg RequestDetailLogSettings, configured bool) error {
	sink, err := s.prepareLogSink(cfg)
	if err != nil {
		return err
	}
	s.installLogSettings(cfg, sink, configured)
	return nil
}

func (s *RequestDetailService) UpdateLogSettings(ctx context.Context, cfg RequestDetailLogSettings) (RequestDetailLogSettingsView, error) {
	if err := cfg.Validate(); err != nil {
		return RequestDetailLogSettingsView{}, err
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if s.closed {
		return RequestDetailLogSettingsView{}, errors.New("request detail service is stopped")
	}
	if s.settingRepo == nil || !s.settingsLoaded {
		return RequestDetailLogSettingsView{}, errors.New("request detail settings storage unavailable")
	}
	cfg.Path = s.logSettings.Path // clients cannot change filesystem destinations
	cfg.Source = strings.TrimSpace(cfg.Source)
	sink, err := s.prepareLogSink(cfg)
	if err != nil {
		return RequestDetailLogSettingsView{}, err
	}
	raw, err := json.Marshal(cfg)
	if err == nil {
		err = s.settingRepo.Set(ctx, requestDetailSettingsKey, string(raw))
	}
	if err != nil {
		if sink != nil && sink != s.persistent {
			close(sink.queue)
			<-sink.done
		}
		return RequestDetailLogSettingsView{}, err
	}
	s.installLogSettings(cfg, sink, true)
	return s.GetLogSettings(), nil
}

func (s *RequestDetailService) syncLogSettings(ctx context.Context) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	cfg, err := readRequestDetailSettings(ctx, s.settingRepo)
	if err != nil {
		s.persistentMu.Lock()
		s.runtimeError = "load request detail settings: " + err.Error()
		s.persistentMu.Unlock()
		return err // retain the last successfully applied configuration
	}
	configured := cfg != nil
	if cfg == nil {
		baseline := requestDetailSettingsFromEnv()
		cfg = &baseline
	}
	if !s.settingsLoaded {
		if localPath := strings.TrimSpace(os.Getenv(requestDetailLogPathEnv)); localPath != "" {
			cfg.Path = localPath
		} else if cfg.Path == "" {
			cfg.Path = defaultRequestDetailLogPath
		}
		s.persistentMu.Lock()
		s.logSettings, s.configured = *cfg, configured
		s.persistentMu.Unlock()
	} else {
		cfg.Path = s.logSettings.Path // each instance retains its local mount
	}
	if s.settingsLoaded && *cfg == s.logSettings && configured == s.configured && s.runtimeError == "" {
		return nil
	}
	s.settingsLoaded = true
	if err := s.applyLogSettings(*cfg, configured); err != nil {
		s.persistentMu.Lock()
		s.runtimeError = err.Error()
		s.persistentMu.Unlock()
		return err
	}
	return nil
}

func (s *RequestDetailService) refreshLogSettings() {
	defer close(s.stopped)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := s.syncLogSettings(ctx)
			cancel()
			if err != nil {
				log.Printf("refresh request detail settings: %v", err)
			}
		}
	}
}

// Stop drains the bounded disk queue and closes the writer after HTTP shutdown.
func (s *RequestDetailService) Stop() {
	s.stopOnce.Do(func() {
		if s.stop != nil {
			close(s.stop)
			<-s.stopped
		}
		s.settingsMu.Lock()
		defer s.settingsMu.Unlock()
		s.closed = true
		s.persistentMu.Lock()
		sink := s.persistent
		s.persistent, s.recording = nil, false
		if sink != nil {
			close(sink.queue)
		}
		s.persistentMu.Unlock()
		if sink != nil {
			<-sink.done
		}
	})
}
