package repository

import (
	"context"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// capturingSqlmockMatcher 接受任意语句，并把真正下发的 SQL 留下来给测试断言。
//
// 这样不必在 ExpectExec 里写整条正则：本组测试只关心 UPDATE 的 SET 子句是否
// 显式带了 `version_id = NULL`，而 SET 子句的顺序属于 ent 的实现细节。
type capturingSqlmockMatcher struct {
	mu   sync.Mutex
	sqls []string
}

func (m *capturingSqlmockMatcher) Match(_, actual string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sqls = append(m.sqls, actual)
	return nil
}

func (m *capturingSqlmockMatcher) last(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.sqls, "repo 没有下发任何 SQL")
	return m.sqls[len(m.sqls)-1]
}

func newPromptBindingRepoTestRepo(t *testing.T, matcher *capturingSqlmockMatcher) (*promptTemplateRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err, "初始化 sqlmock")
	t.Cleanup(func() { _ = db.Close() })

	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	return &promptTemplateRepository{client: client}, mock
}

// 回归：分组从「绑定版本」切到「已禁用」时，UPDATE 必须显式清空 version_id。
//
// ent 的 SetNillableVersionID(nil) 语义是「跳过该列」而不是「置为 NULL」，
// 所以旧实现会把 version_id 留在行上，撞上
// group_prompt_bindings_mode_version_pair（mode=disabled 要求 version_id IS NULL），
// 接口因此返回 500，管理员无法解绑。
func TestUpsertGroupBindingClearsVersionIDWhenDisabled(t *testing.T) {
	matcher := &capturingSqlmockMatcher{}
	repo, mock := newPromptBindingRepoTestRepo(t, matcher)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	binding := &service.GroupPromptBinding{
		GroupID: 52,
		Mode:    service.PromptBindingModeDisabled,
	}

	require.NoError(t, repo.UpsertGroupBinding(context.Background(), binding, 2, false))

	sql := matcher.last(t)
	require.Contains(t, sql, `"version_id" = NULL`,
		"mode=disabled 的 UPDATE 必须显式清空 version_id，否则违反 mode/version 配套 CHECK 约束")
	require.Contains(t, sql, `"mode"`)
	require.Equal(t, 3, binding.Revision, "更新成功后 revision 应递增")
}

// 正向保护：绑定版本时 version_id 仍要写入实际值，不能被上面的清空逻辑误伤。
func TestUpsertGroupBindingWritesVersionIDWhenBound(t *testing.T) {
	matcher := &capturingSqlmockMatcher{}
	repo, mock := newPromptBindingRepoTestRepo(t, matcher)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	versionID := int64(10)
	binding := &service.GroupPromptBinding{
		GroupID:   52,
		Mode:      service.PromptBindingModeVersion,
		VersionID: &versionID,
	}

	require.NoError(t, repo.UpsertGroupBinding(context.Background(), binding, 1, false))

	sql := matcher.last(t)
	require.NotContains(t, sql, `"version_id" = NULL`, "绑定版本时不能把 version_id 置空")
	require.Contains(t, sql, `"version_id" = $`)
	require.Equal(t, 2, binding.Revision)
}

// 乐观锁语义保持不变：行不在或 revision 不匹配时返回冲突而不是静默成功。
func TestUpsertGroupBindingZeroAffectedIsRevisionConflict(t *testing.T) {
	matcher := &capturingSqlmockMatcher{}
	repo, mock := newPromptBindingRepoTestRepo(t, matcher)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 0))

	binding := &service.GroupPromptBinding{
		GroupID: 52,
		Mode:    service.PromptBindingModeDisabled,
	}

	err := repo.UpsertGroupBinding(context.Background(), binding, 2, false)
	require.ErrorIs(t, err, service.ErrPromptRevisionConflict)
}

// 账号覆盖同属一类缺陷：inherit/disabled 都必须清空 version_id。
func TestUpsertAccountOverrideClearsVersionIDWhenInherit(t *testing.T) {
	matcher := &capturingSqlmockMatcher{}
	repo, mock := newPromptBindingRepoTestRepo(t, matcher)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	override := &service.AccountGroupPromptOverride{
		AccountID: 7,
		GroupID:   52,
		Mode:      service.PromptOverrideModeInherit,
	}

	require.NoError(t, repo.UpsertAccountOverride(context.Background(), override, 1, false))

	sql := matcher.last(t)
	require.Contains(t, sql, `"version_id" = NULL`,
		"mode=inherit 的 UPDATE 必须显式清空 version_id，否则违反 mode/version 配套 CHECK 约束")
	require.Equal(t, 2, override.Revision)
}

// 正向保护：账号覆盖指定版本时仍写入实际 version_id。
func TestUpsertAccountOverrideWritesVersionIDWhenBound(t *testing.T) {
	matcher := &capturingSqlmockMatcher{}
	repo, mock := newPromptBindingRepoTestRepo(t, matcher)
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 1))

	versionID := int64(9)
	override := &service.AccountGroupPromptOverride{
		AccountID: 7,
		GroupID:   52,
		Mode:      service.PromptOverrideModeVersion,
		VersionID: &versionID,
	}

	require.NoError(t, repo.UpsertAccountOverride(context.Background(), override, 1, false))

	sql := matcher.last(t)
	require.NotContains(t, sql, `"version_id" = NULL`)
	require.Contains(t, sql, `"version_id" = $`)
	require.Equal(t, 2, override.Revision)
}
