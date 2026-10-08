package repository

import (
	"context"
	"fmt"
	"math"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/accountgrouppromptoverride"
	"github.com/Wei-Shaw/sub2api/ent/grouppromptbinding"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/promptadminevent"
	"github.com/Wei-Shaw/sub2api/ent/promptrequestevent"
	"github.com/Wei-Shaw/sub2api/ent/prompttemplate"
	"github.com/Wei-Shaw/sub2api/ent/prompttemplatedraft"
	"github.com/Wei-Shaw/sub2api/ent/prompttemplateversion"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// promptTemplateRepository 实现配置面（模板/草稿/版本/绑定/覆盖/审计）的持久化。
//
// 所有方法都通过 clientFromContext 取 client，因此在 WithTx 建立的事务上下文里
// 会自动复用同一个事务。
type promptTemplateRepository struct {
	client *dbent.Client
}

// NewPromptTemplateRepository 构造文本提示词配置仓储。
func NewPromptTemplateRepository(client *dbent.Client) service.PromptTemplateRepository {
	return &promptTemplateRepository{client: client}
}

// NewPromptRequestEventRecorder 单独暴露运行期记录端口。
//
// 与配置仓储共用同一实现（无状态，只是包了同一个 ent client），但以独立接口类型
// 提供给网关服务，避免为了注入一个可选记录器而改动 NewOpenAIGatewayService 的长签名。
func NewPromptRequestEventRecorder(client *dbent.Client) service.PromptRequestEventRecorder {
	return &promptTemplateRepository{client: client}
}

func (r *promptTemplateRepository) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return fn(ctx)
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin prompt template transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prompt template transaction: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- 模板

func (r *promptTemplateRepository) ListTemplates(ctx context.Context, includeArchived bool) ([]service.PromptTemplate, error) {
	q := clientFromContext(ctx, r.client).PromptTemplate.Query().
		Order(dbent.Desc(prompttemplate.FieldID))
	if !includeArchived {
		q = q.Where(prompttemplate.ArchivedAtIsNil())
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.PromptTemplate, 0, len(rows))
	for _, row := range rows {
		out = append(out, promptTemplateEntityToService(row))
	}
	return out, nil
}

func (r *promptTemplateRepository) GetTemplate(ctx context.Context, id int64) (*service.PromptTemplate, error) {
	row, err := clientFromContext(ctx, r.client).PromptTemplate.Get(ctx, id)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrPromptTemplateNotFound, nil)
	}
	out := promptTemplateEntityToService(row)
	return &out, nil
}

func (r *promptTemplateRepository) CreateTemplate(ctx context.Context, t *service.PromptTemplate) error {
	if t == nil {
		return service.ErrPromptTemplateNotFound
	}
	builder := clientFromContext(ctx, r.client).PromptTemplate.Create().
		SetName(t.Name).
		SetRevision(t.Revision)
	if t.Description != "" {
		builder = builder.SetDescription(t.Description)
	}
	if t.SourceURL != "" {
		builder = builder.SetSourceURL(t.SourceURL)
	}
	if t.SourceNote != "" {
		builder = builder.SetSourceNote(t.SourceNote)
	}
	if t.CreatedBy != nil {
		builder = builder.SetCreatedBy(*t.CreatedBy)
	}
	if t.UpdatedBy != nil {
		builder = builder.SetUpdatedBy(*t.UpdatedBy)
	}
	created, err := builder.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, service.ErrPromptTemplateNameExists)
	}
	*t = promptTemplateEntityToService(created)
	return nil
}

func (r *promptTemplateRepository) UpdateTemplate(ctx context.Context, t *service.PromptTemplate, expectedRevision int) error {
	if t == nil {
		return service.ErrPromptTemplateNotFound
	}
	affected, err := clientFromContext(ctx, r.client).PromptTemplate.Update().
		Where(
			prompttemplate.IDEQ(t.ID),
			prompttemplate.RevisionEQ(expectedRevision),
		).
		SetName(t.Name).
		SetNillableDescription(nilIfEmpty(t.Description)).
		SetNillableSourceURL(nilIfEmpty(t.SourceURL)).
		SetNillableSourceNote(nilIfEmpty(t.SourceNote)).
		SetNillableUpdatedBy(t.UpdatedBy).
		SetRevision(expectedRevision + 1).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, service.ErrPromptTemplateNameExists)
	}
	if affected == 0 {
		return service.ErrPromptRevisionConflict
	}
	t.Revision = expectedRevision + 1
	return nil
}

func (r *promptTemplateRepository) ArchiveTemplate(ctx context.Context, id int64, expectedRevision int, actor *int64) error {
	affected, err := clientFromContext(ctx, r.client).PromptTemplate.Update().
		Where(
			prompttemplate.IDEQ(id),
			prompttemplate.RevisionEQ(expectedRevision),
			prompttemplate.ArchivedAtIsNil(),
		).
		SetArchivedAt(time.Now()).
		SetNillableUpdatedBy(actor).
		SetRevision(expectedRevision + 1).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrPromptRevisionConflict
	}
	return nil
}

// ---------------------------------------------------------------- 草稿

func (r *promptTemplateRepository) GetDraft(ctx context.Context, templateID int64) (*service.PromptTemplateDraft, error) {
	row, err := clientFromContext(ctx, r.client).PromptTemplateDraft.Query().
		Where(prompttemplatedraft.TemplateIDEQ(templateID)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrPromptDraftNotFound, nil)
	}
	out := promptDraftEntityToService(row)
	return &out, nil
}

func (r *promptTemplateRepository) UpsertDraft(ctx context.Context, d *service.PromptTemplateDraft, expectedRevision int, create bool) error {
	if d == nil {
		return service.ErrPromptDraftNotFound
	}
	client := clientFromContext(ctx, r.client)
	if create {
		created, err := client.PromptTemplateDraft.Create().
			SetTemplateID(d.TemplateID).
			SetBody(d.Body).
			SetClientModels(d.ClientModels).
			SetUpstreamModels(d.UpstreamModels).
			SetSupportedProfiles(d.SupportedProfiles).
			SetRevision(1).
			SetNillableUpdatedBy(d.UpdatedBy).
			Save(ctx)
		if err != nil {
			// 并发创建同一个模板的草稿：唯一索引冲突等价于“别人刚改过”。
			return translatePersistenceError(err, nil, service.ErrPromptRevisionConflict)
		}
		*d = promptDraftEntityToService(created)
		return nil
	}

	affected, err := client.PromptTemplateDraft.Update().
		Where(
			prompttemplatedraft.TemplateIDEQ(d.TemplateID),
			prompttemplatedraft.RevisionEQ(expectedRevision),
		).
		SetBody(d.Body).
		SetClientModels(d.ClientModels).
		SetUpstreamModels(d.UpstreamModels).
		SetSupportedProfiles(d.SupportedProfiles).
		SetNillableUpdatedBy(d.UpdatedBy).
		SetRevision(expectedRevision + 1).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrPromptRevisionConflict
	}
	d.Revision = expectedRevision + 1
	return nil
}

// ---------------------------------------------------------------- 版本

func (r *promptTemplateRepository) ListVersions(ctx context.Context, templateID int64) ([]service.PromptTemplateVersion, error) {
	rows, err := clientFromContext(ctx, r.client).PromptTemplateVersion.Query().
		Where(prompttemplateversion.TemplateIDEQ(templateID)).
		Order(dbent.Desc(prompttemplateversion.FieldVersionNo)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.PromptTemplateVersion, 0, len(rows))
	for _, row := range rows {
		out = append(out, promptVersionEntityToService(row))
	}
	return out, nil
}

func (r *promptTemplateRepository) GetVersion(ctx context.Context, id int64) (*service.PromptTemplateVersion, error) {
	row, err := clientFromContext(ctx, r.client).PromptTemplateVersion.Get(ctx, id)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrPromptVersionNotFound, nil)
	}
	out := promptVersionEntityToService(row)
	return &out, nil
}

func (r *promptTemplateRepository) GetVersionByIdempotencyKey(ctx context.Context, key string) (*service.PromptTemplateVersion, error) {
	if key == "" {
		return nil, service.ErrPromptVersionNotFound
	}
	row, err := clientFromContext(ctx, r.client).PromptTemplateVersion.Query().
		Where(prompttemplateversion.IdempotencyKeyEQ(key)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrPromptVersionNotFound, nil)
	}
	out := promptVersionEntityToService(row)
	return &out, nil
}

func (r *promptTemplateRepository) PublishVersion(ctx context.Context, v *service.PromptTemplateVersion) error {
	if v == nil {
		return service.ErrPromptVersionNotFound
	}
	builder := clientFromContext(ctx, r.client).PromptTemplateVersion.Create().
		SetTemplateID(v.TemplateID).
		SetVersionNo(v.VersionNo).
		SetBody(v.Body).
		SetBodySha256(v.BodySHA256).
		SetBodyBytes(v.BodyBytes).
		SetClientModels(v.ClientModels).
		SetUpstreamModels(v.UpstreamModels).
		SetSupportedProfiles(v.SupportedProfiles).
		SetManifestSha256(v.ManifestSHA256).
		SetPublishedAt(v.PublishedAt)
	if v.ChangeNote != "" {
		builder = builder.SetChangeNote(v.ChangeNote)
	}
	if v.IdempotencyKey != nil {
		builder = builder.SetIdempotencyKey(*v.IdempotencyKey)
	}
	if v.PublishedBy != nil {
		builder = builder.SetPublishedBy(*v.PublishedBy)
	}
	created, err := builder.Save(ctx)
	if err != nil {
		// 并发发布可能撞上 (template_id, version_no) 或 idempotency_key 唯一索引。
		return translatePersistenceError(err, nil, service.ErrPromptRevisionConflict)
	}
	*v = promptVersionEntityToService(created)
	return nil
}

func (r *promptTemplateRepository) NextVersionNo(ctx context.Context, templateID int64) (int, error) {
	rows, err := clientFromContext(ctx, r.client).PromptTemplateVersion.Query().
		Where(prompttemplateversion.TemplateIDEQ(templateID)).
		Order(dbent.Desc(prompttemplateversion.FieldVersionNo)).
		Limit(1).
		All(ctx)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 1, nil
	}
	return rows[0].VersionNo + 1, nil
}

func (r *promptTemplateRepository) LockTemplate(ctx context.Context, id int64) error {
	_, err := clientFromContext(ctx, r.client).PromptTemplate.Query().
		Where(prompttemplate.IDEQ(id)).
		ForUpdate().
		Only(ctx)
	return translatePersistenceError(err, service.ErrPromptTemplateNotFound, nil)
}

// ---------------------------------------------------------------- 绑定

func (r *promptTemplateRepository) GetGroupBinding(ctx context.Context, groupID int64) (*service.GroupPromptBinding, error) {
	row, err := clientFromContext(ctx, r.client).GroupPromptBinding.Query().
		Where(grouppromptbinding.GroupIDEQ(groupID)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrPromptBindingNotFound, nil)
	}
	out := groupBindingEntityToService(row)
	return &out, nil
}

func (r *promptTemplateRepository) UpsertGroupBinding(ctx context.Context, b *service.GroupPromptBinding, expectedRevision int, create bool) error {
	if b == nil {
		return service.ErrPromptBindingNotFound
	}
	client := clientFromContext(ctx, r.client)
	if create {
		created, err := client.GroupPromptBinding.Create().
			SetGroupID(b.GroupID).
			SetMode(b.Mode).
			SetNillableVersionID(b.VersionID).
			SetRevision(1).
			SetNillableUpdatedBy(b.UpdatedBy).
			Save(ctx)
		if err != nil {
			return translatePersistenceError(err, nil, service.ErrPromptRevisionConflict)
		}
		*b = groupBindingEntityToService(created)
		return nil
	}

	affected, err := client.GroupPromptBinding.Update().
		Where(
			grouppromptbinding.GroupIDEQ(b.GroupID),
			grouppromptbinding.RevisionEQ(expectedRevision),
		).
		SetMode(b.Mode).
		SetNillableVersionID(b.VersionID).
		SetNillableUpdatedBy(b.UpdatedBy).
		SetRevision(expectedRevision + 1).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrPromptRevisionConflict
	}
	b.Revision = expectedRevision + 1
	return nil
}

func (r *promptTemplateRepository) DeleteGroupBinding(ctx context.Context, groupID int64) error {
	_, err := clientFromContext(ctx, r.client).GroupPromptBinding.Delete().
		Where(grouppromptbinding.GroupIDEQ(groupID)).
		Exec(ctx)
	return err
}

func (r *promptTemplateRepository) ListBindingsByGroupIDs(ctx context.Context, groupIDs []int64) ([]service.GroupPromptBinding, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	rows, err := clientFromContext(ctx, r.client).GroupPromptBinding.Query().
		Where(grouppromptbinding.GroupIDIn(groupIDs...)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.GroupPromptBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupBindingEntityToService(row))
	}
	return out, nil
}

// ---------------------------------------------------------------- 账号覆盖

func (r *promptTemplateRepository) GetAccountOverride(ctx context.Context, accountID, groupID int64) (*service.AccountGroupPromptOverride, error) {
	row, err := clientFromContext(ctx, r.client).AccountGroupPromptOverride.Query().
		Where(
			accountgrouppromptoverride.AccountIDEQ(accountID),
			accountgrouppromptoverride.GroupIDEQ(groupID),
		).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrPromptOverrideNotFound, nil)
	}
	out := accountOverrideEntityToService(row)
	return &out, nil
}

func (r *promptTemplateRepository) UpsertAccountOverride(ctx context.Context, o *service.AccountGroupPromptOverride, expectedRevision int, create bool) error {
	if o == nil {
		return service.ErrPromptOverrideNotFound
	}
	client := clientFromContext(ctx, r.client)
	if create {
		created, err := client.AccountGroupPromptOverride.Create().
			SetAccountID(o.AccountID).
			SetGroupID(o.GroupID).
			SetMode(o.Mode).
			SetNillableVersionID(o.VersionID).
			SetRevision(1).
			SetNillableUpdatedBy(o.UpdatedBy).
			Save(ctx)
		if err != nil {
			// 覆盖依赖 account_groups 复合外键：账号不在此分组时会违反外键约束。
			return translatePersistenceError(err, nil, service.ErrPromptRevisionConflict)
		}
		*o = accountOverrideEntityToService(created)
		return nil
	}

	affected, err := client.AccountGroupPromptOverride.Update().
		Where(
			accountgrouppromptoverride.AccountIDEQ(o.AccountID),
			accountgrouppromptoverride.GroupIDEQ(o.GroupID),
			accountgrouppromptoverride.RevisionEQ(expectedRevision),
		).
		SetMode(o.Mode).
		SetNillableVersionID(o.VersionID).
		SetNillableUpdatedBy(o.UpdatedBy).
		SetRevision(expectedRevision + 1).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrPromptRevisionConflict
	}
	o.Revision = expectedRevision + 1
	return nil
}

func (r *promptTemplateRepository) DeleteAccountOverride(ctx context.Context, accountID, groupID int64) error {
	_, err := clientFromContext(ctx, r.client).AccountGroupPromptOverride.Delete().
		Where(
			accountgrouppromptoverride.AccountIDEQ(accountID),
			accountgrouppromptoverride.GroupIDEQ(groupID),
		).
		Exec(ctx)
	return err
}

func (r *promptTemplateRepository) ListAccountOverridesByGroup(ctx context.Context, groupID int64) ([]service.AccountGroupPromptOverride, error) {
	rows, err := clientFromContext(ctx, r.client).AccountGroupPromptOverride.Query().
		Where(accountgrouppromptoverride.GroupIDEQ(groupID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.AccountGroupPromptOverride, 0, len(rows))
	for _, row := range rows {
		out = append(out, accountOverrideEntityToService(row))
	}
	return out, nil
}

// ---------------------------------------------------------------- 审计

func (r *promptTemplateRepository) CreateAdminEvent(ctx context.Context, e *service.PromptAdminEvent) error {
	if e == nil {
		return nil
	}
	builder := clientFromContext(ctx, r.client).PromptAdminEvent.Create().
		SetAction(e.Action).
		SetScope(e.Scope).
		SetNillableTemplateID(e.TemplateID).
		SetNillableVersionID(e.VersionID).
		SetNillableGroupID(e.GroupID).
		SetNillableAccountID(e.AccountID).
		SetNillableActorID(e.ActorID)
	if e.ActorName != "" {
		builder = builder.SetActorName(e.ActorName)
	}
	if e.BeforeState != nil {
		builder = builder.SetBeforeState(e.BeforeState)
	}
	if e.AfterState != nil {
		builder = builder.SetAfterState(e.AfterState)
	}
	if e.RequestID != "" {
		builder = builder.SetRequestID(e.RequestID)
	}
	if e.Note != "" {
		builder = builder.SetNote(e.Note)
	}
	created, err := builder.Save(ctx)
	if err != nil {
		return err
	}
	*e = promptAdminEventEntityToService(created)
	return nil
}

func (r *promptTemplateRepository) ListAdminEvents(ctx context.Context, templateID *int64, groupID *int64, limit int) ([]service.PromptAdminEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := clientFromContext(ctx, r.client).PromptAdminEvent.Query().
		Order(dbent.Desc(promptadminevent.FieldID)).
		Limit(limit)
	if templateID != nil {
		q = q.Where(promptadminevent.TemplateIDEQ(*templateID))
	}
	if groupID != nil {
		q = q.Where(promptadminevent.GroupIDEQ(*groupID))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.PromptAdminEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, promptAdminEventEntityToService(row))
	}
	return out, nil
}

// ---------------------------------------------------------------- 映射

func promptTemplateEntityToService(row *dbent.PromptTemplate) service.PromptTemplate {
	if row == nil {
		return service.PromptTemplate{}
	}
	return service.PromptTemplate{
		ID:          row.ID,
		Name:        row.Name,
		Description: derefString(row.Description),
		SourceURL:   derefString(row.SourceURL),
		SourceNote:  derefString(row.SourceNote),
		ArchivedAt:  row.ArchivedAt,
		Revision:    row.Revision,
		CreatedBy:   row.CreatedBy,
		UpdatedBy:   row.UpdatedBy,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func promptDraftEntityToService(row *dbent.PromptTemplateDraft) service.PromptTemplateDraft {
	if row == nil {
		return service.PromptTemplateDraft{}
	}
	return service.PromptTemplateDraft{
		ID:                row.ID,
		TemplateID:        row.TemplateID,
		Body:              row.Body,
		ClientModels:      row.ClientModels,
		UpstreamModels:    row.UpstreamModels,
		SupportedProfiles: row.SupportedProfiles,
		Revision:          row.Revision,
		UpdatedBy:         row.UpdatedBy,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func promptVersionEntityToService(row *dbent.PromptTemplateVersion) service.PromptTemplateVersion {
	if row == nil {
		return service.PromptTemplateVersion{}
	}
	return service.PromptTemplateVersion{
		ID:                row.ID,
		TemplateID:        row.TemplateID,
		VersionNo:         row.VersionNo,
		Body:              row.Body,
		BodySHA256:        row.BodySha256,
		BodyBytes:         row.BodyBytes,
		ClientModels:      row.ClientModels,
		UpstreamModels:    row.UpstreamModels,
		SupportedProfiles: row.SupportedProfiles,
		ManifestSHA256:    row.ManifestSha256,
		ChangeNote:        derefString(row.ChangeNote),
		IdempotencyKey:    row.IdempotencyKey,
		PublishedBy:       row.PublishedBy,
		PublishedAt:       row.PublishedAt,
		CreatedAt:         row.CreatedAt,
	}
}

func groupBindingEntityToService(row *dbent.GroupPromptBinding) service.GroupPromptBinding {
	if row == nil {
		return service.GroupPromptBinding{}
	}
	return service.GroupPromptBinding{
		ID:        row.ID,
		GroupID:   row.GroupID,
		Mode:      row.Mode,
		VersionID: row.VersionID,
		Revision:  row.Revision,
		UpdatedBy: row.UpdatedBy,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func accountOverrideEntityToService(row *dbent.AccountGroupPromptOverride) service.AccountGroupPromptOverride {
	if row == nil {
		return service.AccountGroupPromptOverride{}
	}
	return service.AccountGroupPromptOverride{
		ID:        row.ID,
		AccountID: row.AccountID,
		GroupID:   row.GroupID,
		Mode:      row.Mode,
		VersionID: row.VersionID,
		Revision:  row.Revision,
		UpdatedBy: row.UpdatedBy,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func promptAdminEventEntityToService(row *dbent.PromptAdminEvent) service.PromptAdminEvent {
	if row == nil {
		return service.PromptAdminEvent{}
	}
	return service.PromptAdminEvent{
		ID:          row.ID,
		Action:      row.Action,
		Scope:       row.Scope,
		TemplateID:  row.TemplateID,
		VersionID:   row.VersionID,
		GroupID:     row.GroupID,
		AccountID:   row.AccountID,
		ActorID:     row.ActorID,
		ActorName:   derefString(row.ActorName),
		BeforeState: row.BeforeState,
		AfterState:  row.AfterState,
		RequestID:   derefString(row.RequestID),
		Note:        derefString(row.Note),
		CreatedAt:   row.CreatedAt,
	}
}

// nilIfEmpty 把空字符串转成 nil，避免把“清空描述”写成空串。
func nilIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// ---------------------------------------------------------------- 运行期记录

// CreateRequestEvent 写入一条运行期策略记录。
func (r *promptTemplateRepository) CreateRequestEvent(ctx context.Context, e *service.PromptRequestEvent) error {
	if e == nil {
		return nil
	}
	reason := e.Reason
	if reason == "" {
		reason = service.PromptReasonDisabled
	}
	builder := clientFromContext(ctx, r.client).PromptRequestEvent.Create().
		SetAttemptNo(max(e.AttemptNo, 1)).
		SetApplied(e.Applied).
		SetReason(reason).
		SetAddedBytes(max(e.AddedBytes, 0)).
		SetApplyDurationMs(max(e.ApplyDurationMs, 0)).
		SetNillableGroupID(e.GroupID).
		SetNillableAccountID(e.AccountID).
		SetNillableVersionID(e.VersionID)
	if e.RequestID != "" {
		builder = builder.SetRequestID(e.RequestID)
	}
	if e.ClientModel != "" {
		builder = builder.SetClientModel(e.ClientModel)
	}
	if e.UpstreamModel != "" {
		builder = builder.SetUpstreamModel(e.UpstreamModel)
	}
	if e.OutboundProfile != "" {
		builder = builder.SetOutboundProfile(e.OutboundProfile)
	}
	if e.BindingSource != "" {
		builder = builder.SetBindingSource(e.BindingSource)
	}
	if e.ManifestSHA256 != "" {
		builder = builder.SetManifestSha256(e.ManifestSHA256)
	}
	_, err := builder.Save(ctx)
	return err
}

// RecordPromptRequestEvent 实现 service.PromptRequestEventRecorder。
//
// 尽力而为：该调用位于出站热路径，写入失败只记日志，绝不返回错误 ——
// 上游请求可能已经成功，不能因为审计写入失败而重试或失败。
func (r *promptTemplateRepository) RecordPromptRequestEvent(ctx context.Context, event service.PromptRequestEvent) {
	// 使用独立的短超时上下文：客户端可能已经断开，但记录仍应完成。
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := r.CreateRequestEvent(writeCtx, &event); err != nil {
		logger.LegacyPrintf("repository.prompt_request_event",
			"[PromptRequestEvent] record failed: request_id=%s version_id=%v applied=%v reason=%s err=%v",
			event.RequestID, event.VersionID, event.Applied, event.Reason, err)
	}
}

// ListRequestEvents 按条件分页查询运行期记录（新到旧）。
//
// 两种调用方式共存，保证历史调用方不受影响：
//   - 只给 filter.Limit：沿用旧语义（默认 100、上限 500），从最新一条开始取；
//   - 给 filter.Pagination（page/page_size）：按页取，Pagination 优先。
func (r *promptTemplateRepository) ListRequestEvents(ctx context.Context, filter service.PromptRequestEventFilter) ([]service.PromptRequestEvent, *pagination.PaginationResult, error) {
	limit, offset := promptRequestEventWindow(filter)
	preds := promptRequestEventPredicates(filter)

	client := clientFromContext(ctx, r.client)
	total, err := client.PromptRequestEvent.Query().Where(preds...).Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	rows, err := client.PromptRequestEvent.Query().
		Where(preds...).
		Order(dbent.Desc(promptrequestevent.FieldID)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]service.PromptRequestEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, service.PromptRequestEvent{
			ID:              row.ID,
			RequestID:       derefString(row.RequestID),
			AttemptNo:       row.AttemptNo,
			GroupID:         row.GroupID,
			AccountID:       row.AccountID,
			ClientModel:     derefString(row.ClientModel),
			UpstreamModel:   derefString(row.UpstreamModel),
			OutboundProfile: derefString(row.OutboundProfile),
			BindingSource:   derefString(row.BindingSource),
			VersionID:       row.VersionID,
			ManifestSHA256:  derefString(row.ManifestSha256),
			Applied:         row.Applied,
			Reason:          row.Reason,
			AddedBytes:      row.AddedBytes,
			ApplyDurationMs: row.ApplyDurationMs,
			CreatedAt:       row.CreatedAt,
		})
	}
	return out, promptRequestEventPaginationResult(int64(total), filter, limit), nil
}

// 运行期记录列表的条数约束（沿用旧实现的默认值与上限）。
const (
	promptRequestEventDefaultLimit = 100
	promptRequestEventMaxLimit     = 500
)

// promptRequestEventWindow 计算本次查询的 limit/offset。
//
// 抽成纯函数（不触碰数据库），因此 limit/offset 的换算可以在 unit 测试里直接断言。
func promptRequestEventWindow(filter service.PromptRequestEventFilter) (limit, offset int) {
	if filter.Pagination.Page > 0 || filter.Pagination.PageSize > 0 {
		return filter.Pagination.Limit(), filter.Pagination.Offset()
	}
	limit = filter.Limit
	if limit <= 0 || limit > promptRequestEventMaxLimit {
		limit = promptRequestEventDefaultLimit
	}
	return limit, 0
}

// promptRequestEventPredicates 把过滤条件翻译成 ent 谓词。
//
// 同样是纯函数：不依赖数据库，可用 sql.Selector 直接断言生成的 WHERE 子句。
func promptRequestEventPredicates(filter service.PromptRequestEventFilter) []predicate.PromptRequestEvent {
	preds := make([]predicate.PromptRequestEvent, 0, 4)
	if filter.RequestID != nil {
		preds = append(preds, promptrequestevent.RequestIDEQ(*filter.RequestID))
	}
	if filter.GroupID != nil {
		preds = append(preds, promptrequestevent.GroupIDEQ(*filter.GroupID))
	}
	if filter.VersionID != nil {
		preds = append(preds, promptrequestevent.VersionIDEQ(*filter.VersionID))
	}
	if filter.Applied != nil {
		preds = append(preds, promptrequestevent.AppliedEQ(*filter.Applied))
	}
	return preds
}

// promptRequestEventPaginationResult 组装分页元信息。
//
// limit 一定 >= 1（见 promptRequestEventWindow），因此不会出现除零。
func promptRequestEventPaginationResult(total int64, filter service.PromptRequestEventFilter, limit int) *pagination.PaginationResult {
	page := filter.Pagination.Page
	if page < 1 {
		page = 1
	}
	pages := int(math.Ceil(float64(total) / float64(limit)))
	if pages < 1 {
		pages = 1
	}
	return &pagination.PaginationResult{
		Total:    total,
		Page:     page,
		PageSize: limit,
		Pages:    pages,
	}
}
