package repository

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

// 本文件是 Codex 打票仓储的定向单测：SQL 形状、参数绑定、成功流水按 generation
// 去重、失效事件按 generation 去重（ON CONFLICT DO NOTHING）、跨副本 advisory lock。

func TestCodexTicketAttemptRepositoryInsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	occurredAt := time.Now().UTC()
	attempt := &service.CodexTicketAttempt{
		AccountID:          7,
		Model:              "gpt-5.6-sol",
		OccurredAt:         occurredAt,
		Outcome:            "success",
		Trigger:            "manual",
		DurationMS:         120,
		VerificationMethod: "ticket_shape_v1",
	}
	mock.ExpectQuery("INSERT INTO codex_ticket_attempts").
		WithArgs(int64(7), "gpt-5.6-sol", occurredAt, "success", "manual",
			nil, nil, 120, "", nil, "", nil, nil, "ticket_shape_v1", nil, nil, nil, nil).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))

	repo := NewCodexTicketAttemptRepository(db)
	require.NoError(t, repo.Insert(context.Background(), attempt))
	require.Equal(t, int64(42), attempt.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCodexTicketAttemptRepositoryInsertDeduplicatesSuccessGeneration 成功流水的
// ticket_generation_id 有唯一索引：重复写入应被 ON CONFLICT DO NOTHING 静默吞掉。
func TestCodexTicketAttemptRepositoryInsertDeduplicatesSuccessGeneration(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	generation := "gen-1"
	attempt := &service.CodexTicketAttempt{
		AccountID:          7,
		Model:              "gpt-5.6-sol",
		OccurredAt:         time.Now().UTC(),
		Outcome:            "success",
		Trigger:            "automatic",
		TicketGenerationID: &generation,
	}
	mock.ExpectQuery("INSERT INTO codex_ticket_attempts").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "success", "automatic",
			nil, nil, 0, "", nil, "", nil, &generation, "", nil, nil, nil, nil).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	repo := NewCodexTicketAttemptRepository(db)
	require.NoError(t, repo.Insert(context.Background(), attempt))
	require.Zero(t, attempt.ID, "冲突时不应回填 id")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexTicketAttemptRepositoryList(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	occurredAt := time.Now().UTC()
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM codex_ticket_attempts`).
		WithArgs(int64(7), "gpt-5.6-sol").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectQuery("SELECT id,model,occurred_at,outcome,source").
		WithArgs(int64(7), "gpt-5.6-sol", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "model", "occurred_at", "outcome", "source", "http_status", "ticket_length", "duration_ms",
			"reason_code", "proxy_id", "proxy_name", "expires_at", "ticket_generation_id", "verification_method",
			"turn_state_present", "cookie_present", "challenge_expected_count", "parsed_number_count",
		}).
			AddRow(int64(2), "gpt-5.6-sol", occurredAt, "success", "automatic", int64(200), int64(292), 88,
				"", int64(3), "proxy-1", nil, "gen-2", "ticket_shape_v1", true, true, int64(300), int64(300)).
			AddRow(int64(1), "gpt-5.6-sol", occurredAt.Add(-time.Minute), "miss", "manual", int64(500), int64(0), 40,
				"http_status", nil, "", nil, nil, "ticket_shape_v1", false, false, int64(300), int64(0)))

	repo := NewCodexTicketAttemptRepository(db)
	items, total, err := repo.List(context.Background(), 7, "gpt-5.6-sol", false, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, items, 2)
	require.Equal(t, "success", items[0].Outcome)
	require.Equal(t, 200, *items[0].HTTPStatus)
	require.Equal(t, 292, *items[0].TicketLength)
	require.Equal(t, int64(3), *items[0].ProxyID)
	require.Equal(t, "gen-2", *items[0].TicketGenerationID)
	require.Equal(t, "http_status", items[1].ReasonCode)
	require.Nil(t, items[1].TicketGenerationID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexTicketAttemptRepositoryTryLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(true))
	mock.ExpectQuery(`SELECT pg_advisory_unlock`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"unlocked"}).AddRow(true))

	repo := NewCodexTicketAttemptRepository(db)
	unlock, acquired, err := repo.TryLock(context.Background(), 7, "gpt-5.6-sol")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, unlock)
	unlock()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexTicketInvalidationRepositoryInvalidateRemovesCurrentGeneration(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	occurredAt := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->\$2->>'generation_id' FROM accounts`).
		WithArgs(int64(7), "codex_turn_ticket:gpt-5.6-sol").
		WillReturnRows(sqlmock.NewRows([]string{"generation_id"}).AddRow("gen-1"))
	mock.ExpectQuery("INSERT INTO codex_ticket_invalidations").
		WithArgs(int64(7), "gpt-5.6-sol", "gen-1", occurredAt, service.CodexTicketInvalidationCredentialsChanged,
			nil, "user_request", "/v1/responses", true, "original-ticket", "original-cookie", "returned-ticket", nil, nil).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectExec("UPDATE accounts SET extra=COALESCE").
		WithArgs(int64(7), "codex_turn_ticket:gpt-5.6-sol").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := NewCodexTicketInvalidationRepository(db)
	event := &service.CodexTicketInvalidation{
		AccountID:          7,
		Model:              "gpt-5.6-sol",
		TicketGenerationID: "gen-1",
		OccurredAt:         occurredAt,
		ReasonCode:         service.CodexTicketInvalidationCredentialsChanged,
		RequestKind:        "user_request",
		RequestRoute:       "/v1/responses",
		OriginalTicket:     codexTicketStringPtr("original-ticket"),
		OriginalCookie:     codexTicketStringPtr("original-cookie"),
		ReturnedTicket:     "returned-ticket",
	}
	inserted, err := repo.Invalidate(context.Background(), event)
	require.NoError(t, err)
	require.True(t, inserted)
	require.True(t, event.InvalidatedCurrent)
	require.Equal(t, int64(9), event.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCodexTicketInvalidationRepositoryInvalidateDedupes 同一代票据的第二次失效
// 观测不得重复插入（ON CONFLICT DO NOTHING → 走补齐分支 → 返回 false）。
func TestCodexTicketInvalidationRepositoryInvalidateDedupes(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	occurredAt := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->\$2->>'generation_id' FROM accounts`).
		WithArgs(int64(7), "codex_turn_ticket:gpt-5.6-sol").
		WillReturnRows(sqlmock.NewRows([]string{"generation_id"}).AddRow("gen-1"))
	mock.ExpectQuery("INSERT INTO codex_ticket_invalidations").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "gen-1", occurredAt, service.CodexTicketInvalidationCredentialsChanged,
								nil, "user_request", "/v1/responses", true, nil, nil, "returned-ticket", nil, nil).
		WillReturnRows(sqlmock.NewRows([]string{"id"})) // ON CONFLICT DO NOTHING
	mock.ExpectExec("UPDATE codex_ticket_invalidations SET").
		WithArgs("gen-1", occurredAt, nil, "user_request", "/v1/responses", nil, nil, "returned-ticket", nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := NewCodexTicketInvalidationRepository(db)
	event := &service.CodexTicketInvalidation{
		AccountID:          7,
		Model:              "gpt-5.6-sol",
		TicketGenerationID: "gen-1",
		OccurredAt:         occurredAt,
		ReasonCode:         service.CodexTicketInvalidationCredentialsChanged,
		RequestKind:        "user_request",
		RequestRoute:       "/v1/responses",
		ReturnedTicket:     "returned-ticket",
	}
	inserted, err := repo.Invalidate(context.Background(), event)
	require.NoError(t, err)
	require.False(t, inserted, "重复失效事件应被去重")
	require.False(t, event.InvalidatedCurrent)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexTicketInvalidationRepositoryCurrentTicket(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`SELECT COALESCE\(extra->\$2, 'null'::jsonb\)`).
		WithArgs(int64(7), "codex_turn_ticket:gpt-5.6-sol").
		WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`{"state":"gAAAAA"}`)))
	repo := NewCodexTicketInvalidationRepository(db)
	raw, err := repo.CurrentTicket(context.Background(), 7, "gpt-5.6-sol")
	require.NoError(t, err)
	require.JSONEq(t, `{"state":"gAAAAA"}`, string(raw))

	mock.ExpectQuery(`SELECT COALESCE\(extra->\$2, 'null'::jsonb\)`).
		WithArgs(int64(8), "codex_turn_ticket:gpt-5.6-sol").
		WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow([]byte(`null`)))
	raw, err = repo.CurrentTicket(context.Background(), 8, "gpt-5.6-sol")
	require.NoError(t, err)
	require.Nil(t, raw)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAccountRepositoryCodexTicketExtraMerge 验证票据键写入/删除直接走 JSONB 运算，
// 不经过 UpdateExtra（避免每打一发票都触发调度 outbox）。
func TestAccountRepositoryCodexTicketExtraMerge(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	repo := &accountRepository{client: client}

	mock.ExpectExec("UPDATE accounts SET extra = COALESCE").
		WithArgs(`{"codex_turn_ticket:gpt-5.6-sol":{"state":"gAAAAA"}}`, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.UpdateCodexTicketExtra(context.Background(), 7, "codex_turn_ticket:gpt-5.6-sol", map[string]any{"state": "gAAAAA"}))

	mock.ExpectExec("UPDATE accounts SET extra = COALESCE").
		WithArgs("codex_turn_ticket:gpt-5.6-sol", int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, repo.RemoveCodexTicketExtra(context.Background(), 7, "codex_turn_ticket:gpt-5.6-sol"), service.ErrAccountNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCodexTicketAttemptRepositoryLatestEvents 批量最近事件查询绑定 pq.Array。
func TestCodexTicketAttemptRepositoryLatestEvents(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	occurredAt := time.Now().UTC()
	mock.ExpectQuery("SELECT DISTINCT ON \\(account_id\\)").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "model", "kind", "occurred_at"}).
			AddRow(int64(7), "gpt-5.6-sol", "success", occurredAt))
	repo := NewCodexTicketAttemptRepository(db)
	events, err := repo.LatestEvents(context.Background(), []int64{7, 8})
	require.NoError(t, err)
	require.Equal(t, "success", events[7].Kind)
	require.NoError(t, mock.ExpectationsWereMet())
}

// codexTicketStringPtr 是测试内指针辅助（integration tag 下的 stringPtr 不可用）。
func codexTicketStringPtr(value string) *string { return &value }
