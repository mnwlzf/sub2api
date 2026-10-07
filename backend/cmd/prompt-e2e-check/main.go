// 临时端到端校验程序：用真实仓储 + 真实 PostgreSQL 跑一遍
// “建模板 → 改草稿 → 发布版本 → 幂等重放 → 绑定 → 记录运行事件”，
// 重点验证 Ent 写入与不可变 trigger 的交互（这是唯一无法靠单元测试覆盖的风险）。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	// Ent 的默认值（含 TimeMixin 的 created_at/updated_at）在 runtime 包的 init 里注册，
	// 不导入会导致 Save 时空指针 panic。生产入口 cmd/server/main.go 同样导入。
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
)

var failures int

func check(name string, ok bool, detail string) {
	if ok {
		fmt.Printf("  [PASS] %s\n", name)
		return
	}
	failures++
	fmt.Printf("  [FAIL] %s :: %s\n", name, detail)
}

func main() {
	dsn := os.Getenv("PROMPT_E2E_DSN")
	if dsn == "" {
		fmt.Println("PROMPT_E2E_DSN is required")
		os.Exit(2)
	}
	client, err := dbent.Open("postgres", dsn)
	if err != nil {
		fmt.Println("open db:", err)
		os.Exit(2)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	repo := repository.NewPromptTemplateRepository(client)
	svc := service.NewPromptTemplateService(repo)
	actor := service.PromptActor{Name: "e2e@local"}
	runID := time.Now().UnixNano()
	name := fmt.Sprintf("e2e-%d", runID)
	// 幂等键与 request_id 必须每次唯一，否则重跑会命中上一轮的记录，
	// 导致“幂等重放”与计数断言出现假失败。
	idemKey := fmt.Sprintf("e2e-key-%d", runID)
	requestID := fmt.Sprintf("e2e-request-%d", runID)

	// 1. 建模板（同时创建占位草稿）
	tpl, err := svc.CreateTemplate(ctx, service.PromptTemplateInput{Name: name, Description: "e2e"}, actor)
	if err != nil {
		fmt.Println("CreateTemplate:", err)
		os.Exit(1)
	}
	check("创建模板", tpl.ID > 0, fmt.Sprintf("id=%d", tpl.ID))

	// 2. 写草稿
	draft, err := svc.UpdateDraft(ctx, tpl.ID, 1, service.PromptDraftInput{
		Body:              "server-side prompt for e2e",
		ClientModels:      []string{"gpt-6.1-sol"},
		SupportedProfiles: []string{service.PromptProfileChatHTTP},
	}, actor)
	if err != nil {
		fmt.Println("UpdateDraft:", err)
		os.Exit(1)
	}
	check("更新草稿", draft.Revision == 2, fmt.Sprintf("revision=%d", draft.Revision))

	// 3. 发布版本 —— 关键：Ent 插入与不可变 trigger 的交互
	version, err := svc.PublishVersion(ctx, tpl.ID, draft.Revision, "e2e publish", idemKey, actor)
	if err != nil {
		fmt.Println("PublishVersion:", err)
		os.Exit(1)
	}
	check("发布版本（Ent 写入 vs trigger）", version.ID > 0 && version.VersionNo == 1,
		fmt.Sprintf("id=%d no=%d", version.ID, version.VersionNo))
	check("正文摘要与字节数", version.BodySHA256 != "" && version.BodyBytes == len(draft.Body),
		fmt.Sprintf("sha=%s bytes=%d", version.BodySHA256[:8], version.BodyBytes))
	check("manifest 摘要非空", version.ManifestSHA256 != "", "")

	// 4. 幂等重放同一个 idempotency_key
	replayed, err := svc.PublishVersion(ctx, tpl.ID, draft.Revision, "e2e publish", idemKey, actor)
	if err != nil {
		fmt.Println("PublishVersion replay:", err)
		os.Exit(1)
	}
	check("发布幂等重放返回同一版本", replayed.ID == version.ID,
		fmt.Sprintf("first=%d replay=%d", version.ID, replayed.ID))

	// 5. 乐观锁：用过期 revision 发布必须冲突
	_, err = svc.PublishVersion(ctx, tpl.ID, 1, "stale", idemKey+"-2", actor)
	check("过期 draft_revision 被拒绝", errors.Is(err, service.ErrPromptRevisionConflict), fmt.Sprintf("err=%v", err))

	// 6. 分组绑定
	group := createGroup(ctx, client)
	binding, err := svc.SetGroupBinding(ctx, group, 0, service.PromptBindingInput{
		Mode:      service.PromptBindingModeVersion,
		VersionID: &version.ID,
	}, actor)
	if err != nil {
		fmt.Println("SetGroupBinding:", err)
		os.Exit(1)
	}
	check("绑定分组到固定版本", binding.Enabled(), fmt.Sprintf("mode=%s", binding.Mode))

	// 7. 策略解析：确认绑定真的能被解析出来
	resolver := service.NewPromptPolicyResolverWithFlag(repo, func() bool { return true })
	policy, err := resolver.Resolve(ctx, group, 0, "gpt-6.1-sol")
	if err != nil {
		fmt.Println("Resolve:", err)
		os.Exit(1)
	}
	check("策略解析出启用状态", policy.Enabled && policy.VersionID == version.ID,
		fmt.Sprintf("enabled=%v version=%d", policy.Enabled, policy.VersionID))
	check("策略带出正文与 manifest", policy.Body == draft.Body && policy.ManifestSHA256 == version.ManifestSHA256, "")

	// 8. 模型范围不匹配时不注入
	skipped, err := resolver.Resolve(ctx, group, 0, "some-other-model")
	check("模型范围外不注入", err == nil && !skipped.Enabled && skipped.Reason == service.PromptReasonSkippedModelScope,
		fmt.Sprintf("err=%v reason=%s", err, skipped.Reason))

	// 9. 运行期记录写入
	recorder := repository.NewPromptRequestEventRecorder(client)
	versionID := version.ID
	groupID := group
	recorder.RecordPromptRequestEvent(ctx, service.PromptRequestEvent{
		RequestID: requestID, AttemptNo: 1, GroupID: &groupID, VersionID: &versionID,
		OutboundProfile: service.PromptProfileChatHTTP, Applied: true,
		Reason: service.PromptReasonApplied, AddedBytes: 42,
	})
	time.Sleep(200 * time.Millisecond)
	events, err := svc.ListRequestEvents(ctx, service.PromptRequestEventFilter{RequestID: strPtr(requestID)})
	if err != nil {
		fmt.Println("ListRequestEvents:", err)
		os.Exit(1)
	}
	check("运行事件可写入并查回", len(events) == 1 && events[0].Applied && events[0].AddedBytes == 42,
		fmt.Sprintf("count=%d", len(events)))

	// 10. 已发布版本不可修改（trigger 生效，且错误不会伪装成成功）
	err = forceUpdateVersionBody(ctx, client, version.ID)
	check("已发布版本不可修改", err != nil, fmt.Sprintf("err=%v", err))

	// 11. 管理审计确实随配置一起写入。
	// 注意：绑定事件按 group_id 归属（template_id 为空），因此按模板查询只应看到
	// create / update_draft / publish 三条；按分组查询才应看到 set_binding。
	adminEvents, err := svc.ListAdminEvents(ctx, &tpl.ID, nil, 50)
	if err != nil {
		fmt.Println("ListAdminEvents:", err)
		os.Exit(1)
	}
	check("模板维度审计已记录", len(adminEvents) == 3, fmt.Sprintf("count=%d", len(adminEvents)))

	groupEvents, err := svc.ListAdminEvents(ctx, nil, &group, 50)
	if err != nil {
		fmt.Println("ListAdminEvents(group):", err)
		os.Exit(1)
	}
	check("分组维度审计已记录绑定事件", len(groupEvents) >= 1, fmt.Sprintf("count=%d", len(groupEvents)))

	fmt.Printf("\nFAILURES=%d\n", failures)
	if failures > 0 {
		os.Exit(1)
	}
}

func strPtr(v string) *string { return &v }

func createGroup(ctx context.Context, client *dbent.Client) int64 {
	row, err := client.Group.Create().SetName(fmt.Sprintf("e2e-group-%d", time.Now().UnixNano())).Save(ctx)
	if err != nil {
		fmt.Println("create group:", err)
		os.Exit(1)
	}
	return row.ID
}

// forceUpdateVersionBody 直接绕过服务层改已发布版本，验证数据库 trigger 兜底。
func forceUpdateVersionBody(ctx context.Context, client *dbent.Client, versionID int64) error {
	_, err := client.PromptTemplateVersion.UpdateOneID(versionID).SetBody("tampered").Save(ctx)
	return err
}
