package service

import (
	"context"
)

type runtimeSettingRepo struct {
	*codexPolicyMigrationRepoStub
	err error
}

func (r *runtimeSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.codexPolicyMigrationRepoStub.GetValue(ctx, key)
}

// SetMultiple 让 UpdateSettings 可以在本 stub 上落库（父 stub 对该方法 panic）。
func (r *runtimeSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	if r.err != nil {
		return r.err
	}
	for k, v := range settings {
		r.values[k] = v
	}
	return nil
}
