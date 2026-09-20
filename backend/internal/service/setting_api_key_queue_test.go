package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyQueuePanelPolicyAndWSConnectionSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(repo, nil)
	svc := NewConcurrencyService(nil)
	svc.SetAPIKeyQueueSettings(settings)
	svc.ConfigureAPIKeyQueues(map[string]config.APIKeyQueueConfig{"1": {MaxWaiting: 3, TimeoutSeconds: 20}})
	require.Equal(t, 3, svc.resolveAPIKeyQueuePolicy(ctx, 1).maxWaiting)
	require.Zero(t, svc.resolveAPIKeyQueuePolicy(ctx, 2).maxWaiting)
	oldWS, enabled := svc.WithAPIKeyQueuePolicy(ctx, 2)
	require.False(t, enabled)
	on := APIKeyQueueSettings{Enabled: true, MaxWaiting: 6, TimeoutSeconds: 60}
	require.NoError(t, settings.SetAPIKeyQueueSettings(ctx, on))
	for _, id := range []int64{1, 2, 99} {
		require.Equal(t, apiKeyQueuePolicy{6, time.Minute}, svc.resolveAPIKeyQueuePolicy(ctx, id))
	}
	require.Zero(t, svc.resolveAPIKeyQueuePolicy(oldWS, 2).maxWaiting, "old WS without a persistent reader must not start waiting")
	newWS, enabled := svc.WithAPIKeyQueuePolicy(ctx, 2)
	require.True(t, enabled)
	restarted := NewSettingService(repo, nil)
	require.Equal(t, &on, restarted.apiKeyQueueRuntimeSettings(ctx), "policy survives restart")
	off := on
	off.Enabled = false
	require.NoError(t, settings.SetAPIKeyQueueSettings(ctx, off))
	require.Zero(t, svc.resolveAPIKeyQueuePolicy(ctx, 1).maxWaiting, "explicit panel off overrides YAML")
	require.Equal(t, 6, svc.resolveAPIKeyQueuePolicy(newWS, 2).maxWaiting, "existing WS keeps its safe reader/policy pairing")
	// Another instance sees a saved change after its short cache expires.
	restarted.apiKeyQueueCache.Store(&cachedAPIKeyQueueSettings{value: &on, expiresAt: time.Now().Add(-time.Second)})
	require.Equal(t, &off, restarted.apiKeyQueueRuntimeSettings(ctx))
	repo.err = errors.New("database unavailable")
	settings.apiKeyQueueCache.Store(&cachedAPIKeyQueueSettings{value: &off})
	require.Equal(t, &off, settings.apiKeyQueueRuntimeSettings(ctx), "storage failure must retain last known off state")
}

func TestAPIKeyQueuePanelRejectsInvalidSettings(t *testing.T) {
	repo := &codexPolicyMigrationRepoStub{values: map[string]string{}}
	settings := NewSettingService(repo, nil)
	for _, value := range []APIKeyQueueSettings{
		{MaxWaiting: 0, TimeoutSeconds: 20}, {MaxWaiting: 101, TimeoutSeconds: 20},
		{MaxWaiting: 6, TimeoutSeconds: 0}, {MaxWaiting: 6, TimeoutSeconds: 61},
	} {
		require.Error(t, settings.SetAPIKeyQueueSettings(context.Background(), value))
	}
	require.Empty(t, repo.sets)
}
