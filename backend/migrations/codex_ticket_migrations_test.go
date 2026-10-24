package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCodexTicketMigrationsAreAdditive 守护 251-253 的「纯加性、可重复执行」约定：
// 迁移会被迁移器按 checksum 记录，但生产上仍可能因为手工排障被重复执行，
// 因此这里同时检查幂等关键字与「没有破坏性语句」。
func TestCodexTicketMigrationsAreAdditive(t *testing.T) {
	tests := []struct {
		file     string
		contains []string
	}{
		{
			file: "251_codex_ticket_attempts.sql",
			contains: []string{
				"CREATE TABLE IF NOT EXISTS codex_ticket_attempts",
				"outcome TEXT NOT NULL CHECK (outcome IN ('success', 'miss', 'error'))",
				"source TEXT NOT NULL CHECK (source IN ('automatic', 'manual'))",
				"CREATE INDEX IF NOT EXISTS codex_ticket_attempts_account_model_time",
				"CREATE INDEX IF NOT EXISTS codex_ticket_attempts_cleanup",
			},
		},
		{
			file: "252_codex_ticket_attempts_lifecycle.sql",
			contains: []string{
				"ADD COLUMN IF NOT EXISTS ticket_generation_id UUID",
				"ADD COLUMN IF NOT EXISTS turn_state_present BOOLEAN",
				"ADD COLUMN IF NOT EXISTS challenge_expected_count INTEGER",
				"CREATE UNIQUE INDEX IF NOT EXISTS codex_ticket_attempts_success_generation",
			},
		},
		{
			file: "253_codex_ticket_invalidations.sql",
			contains: []string{
				"CREATE TABLE IF NOT EXISTS codex_ticket_invalidations",
				"ticket_generation_id UUID NOT NULL UNIQUE",
				"returned_ticket TEXT NOT NULL",
				"CREATE INDEX IF NOT EXISTS codex_ticket_invalidations_account_model_time",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			content, err := FS.ReadFile(test.file)
			require.NoError(t, err)
			sql := strings.Join(strings.Fields(string(content)), " ")
			for _, fragment := range test.contains {
				require.Contains(t, sql, fragment)
			}
			upper := strings.ToUpper(sql)
			require.NotContains(t, upper, "DROP TABLE")
			require.NotContains(t, upper, "DROP COLUMN")
			require.NotContains(t, upper, "TRUNCATE")
			require.NotContains(t, upper, "DELETE FROM")
		})
	}
}

// TestCodexTicketAttemptsDoesNotStoreSecrets 票据本体/凭证/代理 URL 绝不允许进入
// 可长期保留、可导出的流水表。
func TestCodexTicketAttemptsDoesNotStoreSecrets(t *testing.T) {
	content, err := FS.ReadFile("251_codex_ticket_attempts.sql")
	require.NoError(t, err)
	sql := strings.ToLower(strings.Join(strings.Fields(string(content)), " "))
	require.NotContains(t, sql, " ticket text")
	require.NotContains(t, sql, " state text")
	require.NotContains(t, sql, " proxy_url")
	require.NotContains(t, sql, "access_token")
	require.NotContains(t, sql, "refresh_token")
}
