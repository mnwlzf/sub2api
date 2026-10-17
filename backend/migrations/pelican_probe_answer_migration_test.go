package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPelicanProbeAnswerMigrationIsAdditive(t *testing.T) {
	content, err := FS.ReadFile("250_pelican_probe_answer.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE pelican_probe_results")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS answer_value INT")
	// Nullable on purpose: non-candy tests, unparsable answers and every row
	// written before this column existed must stay valid.
	require.NotContains(t, sql, "NOT NULL")
	require.NotContains(t, strings.ToUpper(sql), "DROP")
}
