package repository

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestUpdatePoolUpstreamInfoSnapshotRequiresSameIdentitySelectionAndSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		affected int64
		wantErr  error
	}{
		{name: "same identity selection and snapshot", affected: 1},
		{name: "identity selection or snapshot changed", affected: 0, wantErr: service.ErrPoolUpstreamInfoIdentityChanged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			driver := entsql.OpenDB(dialect.Postgres, db)
			client := dbent.NewClient(dbent.Driver(driver))
			t.Cleanup(func() { _ = client.Close() })

			mock.ExpectBegin()
			tx, err := client.Tx(context.Background())
			require.NoError(t, err)
			mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+
				regexp.QuoteMeta("WHERE id = $2")+`.*`+
				regexp.QuoteMeta("AND platform = $3")+`.*`+
				regexp.QuoteMeta("AND type = $4")+`.*`+
				regexp.QuoteMeta("AND credentials = $5::jsonb")+`.*`+
				regexp.QuoteMeta(`AND credentials @> '{"pool_mode":true}'::jsonb`)+`.*`+
				regexp.QuoteMeta("AND proxy_id IS NOT DISTINCT FROM $6")+`.*`+
				regexp.QuoteMeta("COALESCE(extra -> 'pool_upstream_info', 'null'::jsonb) = $7::jsonb")+`.*`+
				regexp.QuoteMeta("COALESCE(extra -> 'pool_upstream_platform', 'null'::jsonb) = $8::jsonb")+`.*`+
				regexp.QuoteMeta("COALESCE(extra -> 'pool_upstream_features', 'null'::jsonb) = $9::jsonb")).
				WithArgs(
					sqlmock.AnyArg(), int64(17), service.PlatformAnthropic, service.AccountTypeAPIKey,
					`{"api_key":"sk-pool","base_url":"https://pool.example","pool_mode":true}`, nil,
					"null", `"sub2api"`, `["balance"]`).
				WillReturnResult(sqlmock.NewResult(0, tt.affected))
			if tt.affected > 0 {
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
					WithArgs(service.SchedulerOutboxEventAccountChanged, int64(17), nil, nil, sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(1, 1))
			}
			repo := newAccountRepositoryWithSQL(client, &recordingSQLExecutor{err: errors.New("must use transaction client")}, nil)
			account := &service.Account{
				ID:       17,
				Platform: service.PlatformAnthropic,
				Type:     service.AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key":   "sk-pool",
					"base_url":  "https://pool.example",
					"pool_mode": true,
				},
				Extra: map[string]any{
					service.PoolUpstreamPlatformExtraKey: service.PoolUpstreamPlatformSub2API,
					service.PoolUpstreamFeaturesExtraKey: []string{service.PoolUpstreamFeatureBalance},
				},
			}

			txCtx := dbent.NewTxContext(context.Background(), tx)
			err = repo.UpdatePoolUpstreamInfoSnapshot(txCtx, account, &service.PoolUpstreamInfoSnapshot{Status: service.PoolUpstreamInfoStatusOK})

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUpdatePoolUpstreamInfoSnapshotRejectsChangedProxyIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectBegin()
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT protocol, host, port") + `.*` + regexp.QuoteMeta("FOR SHARE")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
			AddRow("http", "new.example", 3128, "user", "pass", service.StatusActive))

	proxyID := int64(9)
	account := &service.Account{
		ID:          17,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "pool_mode": true},
		ProxyID:     &proxyID,
		Proxy: &service.Proxy{
			ID: proxyID, Protocol: "http", Host: "old.example", Port: 3128,
			Username: "user", Password: "pass", Status: service.StatusActive,
		},
	}
	repo := newAccountRepositoryWithSQL(client, db, nil)
	err = repo.UpdatePoolUpstreamInfoSnapshot(dbent.NewTxContext(context.Background(), tx), account, &service.PoolUpstreamInfoSnapshot{Status: service.PoolUpstreamInfoStatusOK})

	require.ErrorIs(t, err, service.ErrPoolUpstreamInfoIdentityChanged)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryListDuePoolUpstreamInfoAccountsQueryShape(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	var capturedSQL string
	mock.ExpectQuery("WITH candidates AS").
		WithArgs(now, 20).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	repo := newAccountRepositoryWithSQL(nil, captureQuerySQL{db: db, captured: &capturedSQL}, nil)

	accounts, err := repo.ListDuePoolUpstreamInfoAccounts(context.Background(), now, 20)

	require.NoError(t, err)
	require.Empty(t, accounts)
	normalized := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, normalized, "deleted_at IS NULL")
	require.Contains(t, normalized, "status = 'active'")
	require.Contains(t, normalized, "type = 'apikey'")
	require.Contains(t, normalized, "COALESCE(credentials -> 'pool_mode', 'false'::jsonb) = 'true'::jsonb")
	// The declared platform is matched through a per-platform CASE so each
	// branch can enforce its own known feature allowlist.
	require.Contains(t, normalized, "CASE extra ->> 'pool_upstream_platform'")
	require.Contains(t, normalized, "WHEN 'sub2api'")
	require.Contains(t, normalized, "? 'balance'")
	require.Contains(t, normalized, "WHEN 'chatgpt2api'")
	// chatgpt2api is restricted to OpenAI provider accounts.
	require.Contains(t, normalized, "platform = 'openai'")
	require.Contains(t, normalized, "?| ARRAY['account_count', 'image_quota']")
	require.Contains(t, normalized, `jsonb_typeof(extra -> 'pool_upstream_features') = 'array'`)
	require.Contains(t, normalized, "parsed AS MATERIALIZED")
	require.Contains(t, normalized, "parsed_next_probe_at::timestamptz <= $1")
	require.Contains(t, normalized, "LIMIT $2")
	// The due query must not depend on the billing probe switches.
	require.NotContains(t, normalized, "upstream_billing_probe_enabled")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryListDuePoolUpstreamInfoAccountsRejectsNonPositiveLimit(t *testing.T) {
	repo := newAccountRepositoryWithSQL(nil, nil, nil)

	accounts, err := repo.ListDuePoolUpstreamInfoAccounts(context.Background(), time.Now(), 0)

	require.NoError(t, err)
	require.Empty(t, accounts)
}

// expectUpdateCredentialsOldRowLock registers the row-locked read of the
// previous credentials that UpdateCredentials performs before deciding whether
// the pool upstream info snapshot must be dropped.
func expectUpdateCredentialsOldRowLock(mock sqlmock.Sqlmock, id int64, oldCredentials string) {
	mock.ExpectQuery(`(?s)` + regexp.QuoteMeta("SELECT credentials FROM accounts WHERE id = $1 AND deleted_at IS NULL") + `.*` +
		regexp.QuoteMeta("FOR UPDATE")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"credentials"}).AddRow(oldCredentials))
}

// expectPoolUpstreamIdentityCleanup registers the conditional extra cleanup
// UPDATE that runs when the probe identity subset of the credentials changed.
// poolModeOff mirrors credentials["pool_mode"] != true on the new payload.
func expectPoolUpstreamIdentityCleanup(mock sqlmock.Sqlmock, id int64, poolModeOff bool) {
	mock.ExpectExec(`(?s)`+regexp.QuoteMeta("UPDATE accounts")+`.*`+
		regexp.QuoteMeta("- 'pool_upstream_info'")+`.*`+
		regexp.QuoteMeta("extra ? 'pool_upstream_platform'")).
		WithArgs(id, poolModeOff).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// UpdateCredentials must read the old credentials under FOR UPDATE so the
// pool-upstream cleanup decision is based on the same locked row the CASE
// expressions see, and the conditional cleanup UPDATE must normalize the
// selection keys to default+[] when pool_mode was turned off.
func TestUpdateCredentialsPoolUpstreamCleanupRunsUnderRowLock(t *testing.T) {
	tests := []struct {
		name           string
		oldCredentials string
		newCredentials map[string]any
		wantCleanup    bool
	}{
		{
			name:           "api key change drops snapshot and normalizes disabled pool mode",
			oldCredentials: `{"api_key":"sk-old","base_url":"https://pool.example","pool_mode":true}`,
			newCredentials: map[string]any{"api_key": "sk-new", "base_url": "https://pool.example"},
			wantCleanup:    true,
		},
		{
			name:           "unchanged identity keys skip cleanup",
			oldCredentials: `{"api_key":"sk-same","base_url":"https://pool.example","pool_mode":true}`,
			newCredentials: map[string]any{"api_key": "sk-same", "base_url": "https://pool.example", "pool_mode": true},
			wantCleanup:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })

			newCredentialsJSON, err := json.Marshal(tt.newCredentials)
			require.NoError(t, err)
			mock.ExpectBegin()
			expectUpdateCredentialsOldRowLock(mock, 17, tt.oldCredentials)
			mock.ExpectExec(`(?s)UPDATE accounts`).
				WithArgs(string(newCredentialsJSON), int64(17)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			if tt.wantCleanup {
				poolModeOff := tt.newCredentials["pool_mode"] != true
				expectPoolUpstreamIdentityCleanup(mock, 17, poolModeOff)
			}
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
				WithArgs(service.SchedulerOutboxEventAccountChanged, int64(17), nil, nil, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()

			repo := newAccountRepositoryWithSQL(client, db, nil)
			err = repo.UpdateCredentials(context.Background(), 17, tt.newCredentials)

			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
