#!/usr/bin/env bash
# 文本提示词注入：本地端到端测试环境一键搭建。
#
# 用途：把「起服务 + 造数据 + 发请求 + 查遥测」固化成可重复执行的脚本，
# 避免每次排查都要重新摸索表结构、约束与校验顺序。
#
# 用法：
#   bash scripts/prompt-injection-e2e.sh up      # 建库、建配置、起服务、造数据
#   bash scripts/prompt-injection-e2e.sh test    # 发请求并打印遥测
#   bash scripts/prompt-injection-e2e.sh down    # 停服务、删库、清理临时文件
#
# 关键经验（踩过的坑，勿改）：
#   1. 用户余额必须在**首次请求之前**写对。API Key 认证会缓存快照，
#      先以 balance=0 发过请求再改库，缓存仍会让请求 403 INSUFFICIENT_BALANCE。
#      阈值就是 `balance <= 0`（见 middleware/api_key_auth.go:397）。
#   2. prompt_template_versions 有 CHECK body_bytes = octet_length(body)，
#      不要手写字节数，用 octet_length() 计算。
#   3. 插入失败也会消耗 sequence，version id 不一定是 1；绑定要用子查询取真实 id。
#   4. 中文经 shell 传给 psql 会乱码，SQL 一律写入文件后用 -f 执行，正文用 ASCII。
#   5. 绝不要修改 G:\app\data\config.yaml；复制一份到临时目录并设置 CONFIG_FILE。
set -uo pipefail

PSQL="/d/Develop/DevelopEnv/PostgreSQL/15/bin/psql.exe"
export PGPASSWORD="${PGPASSWORD:-postgres}"
PGH="-h 127.0.0.1 -U postgres"
DBNAME="${DBNAME:-s2a_inject_e2e}"
WORKDIR="${WORKDIR:-/tmp/s2a-inject-e2e}"
BIN="/tmp/s2a-inject-e2e.exe"
LOG="/tmp/s2a-inject-e2e.log"
PORT="${PORT:-8090}"

UPSTREAM_BASE="${UPSTREAM_BASE:?需要设置 UPSTREAM_BASE，例如 https://codex.trovebox.online}"
UPSTREAM_KEY="${UPSTREAM_KEY:?需要设置 UPSTREAM_KEY}"
TEST_MODEL="${TEST_MODEL:-gpt-6.1-sol}"
API_KEY_VALUE="sk-inject-e2e-0001"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

die() { echo "错误: $*" >&2; exit 1; }

cmd_up() {
  [ -n "$UPSTREAM_BASE" ] || die "缺少 UPSTREAM_BASE"

  echo "== 1/5 建库 =="
  "$PSQL" $PGH -tAc "DROP DATABASE IF EXISTS $DBNAME;" >/dev/null 2>&1
  "$PSQL" $PGH -tAc "CREATE DATABASE $DBNAME;" >/dev/null || die "建库失败"

  echo "== 2/5 写临时配置（不动用户真实配置） =="
  mkdir -p "$WORKDIR"
  cat > "$WORKDIR/config.yaml" <<EOF
server:
    host: 0.0.0.0
    port: $PORT
    mode: release
database:
    host: 127.0.0.1
    port: 5432
    user: postgres
    password: postgres
    dbname: $DBNAME
    sslmode: disable
redis:
    host: 127.0.0.1
    port: 6379
jwt:
    secret: 056113e2b9ca5edcc8fc9afddf0b980d008c56ccf5d593adb955513d9bc47822
    expire_hour: 24
default:
    user_concurrency: 5
    user_balance: 100000
    api_key_prefix: sk-
    rate_multiplier: 1
timezone: Asia/Shanghai

gateway:
    text_prompt_injection_enabled: true
EOF

  echo "== 3/5 构建并启动 =="
  ( cd "$REPO_ROOT/backend" && go build -o "$BIN" ./cmd/server ) || die "构建失败"
  # 先清掉可能残留的 api key 缓存，避免旧余额快照干扰
  redis-cli FLUSHDB >/dev/null 2>&1 || true
  ( cd "$REPO_ROOT/backend" && CONFIG_FILE="$WORKDIR/config.yaml" "$BIN" > "$LOG" 2>&1 & )
  for _ in $(seq 1 60); do
    curl -s -o /dev/null "http://127.0.0.1:$PORT/health" && break
    sleep 1
  done
  curl -s -o /dev/null -w "健康检查: %{http_code}\n" "http://127.0.0.1:$PORT/health" || die "服务未就绪，见 $LOG"

  echo "== 4/5 造数据 =="
  cat > "$WORKDIR/seed.sql" <<EOF
-- 用户余额必须在首次请求前设好（见文件头第 1 条）
INSERT INTO users(email,password_hash,role,status,balance)
VALUES('e2e@local.test','x','admin','active',100000) RETURNING id;

INSERT INTO groups(name,platform,status) VALUES('e2e-A','openai','active') RETURNING id;
INSERT INTO groups(name,platform,status) VALUES('e2e-B','openai','active') RETURNING id;

INSERT INTO accounts(name,platform,type,status,credentials)
VALUES('e2e-upstream','openai','apikey','active',
       jsonb_build_object('api_key','$UPSTREAM_KEY','base_url','$UPSTREAM_BASE')) RETURNING id;

INSERT INTO account_groups(account_id,group_id)
SELECT (SELECT max(id) FROM accounts), (SELECT id FROM groups WHERE name='e2e-A');
INSERT INTO account_groups(account_id,group_id)
SELECT (SELECT max(id) FROM accounts), (SELECT id FROM groups WHERE name='e2e-B');

INSERT INTO api_keys(user_id,key,name,group_id,status,quota)
VALUES((SELECT max(id) FROM users),'$API_KEY_VALUE','e2e-key-A',(SELECT id FROM groups WHERE name='e2e-A'),'active',100000);
INSERT INTO api_keys(user_id,key,name,group_id,status,quota)
VALUES((SELECT max(id) FROM users),'sk-inject-e2e-0002','e2e-key-B',(SELECT id FROM groups WHERE name='e2e-B'),'active',100000);

INSERT INTO prompt_templates(name,revision) VALUES('e2e-marker',1);

-- 字节数必须用 octet_length 计算（见文件头第 2 条）
INSERT INTO prompt_template_versions(
    template_id,version_no,body,body_sha256,body_bytes,
    client_models,upstream_models,supported_profiles,manifest_sha256,published_at)
SELECT (SELECT max(id) FROM prompt_templates),1,
       'Before answering anything, first output one line: INJECTION_MARKER_E2E. Then answer normally.',
       repeat('a',64),
       octet_length('Before answering anything, first output one line: INJECTION_MARKER_E2E. Then answer normally.'),
       jsonb_build_array('$TEST_MODEL'),'[]'::jsonb,jsonb_build_array('chat_http'),repeat('b',64),now();

-- 绑定用子查询取真实 version id（见文件头第 3 条）
INSERT INTO group_prompt_bindings(group_id,mode,version_id,revision)
SELECT (SELECT id FROM groups WHERE name='e2e-A'),'version',(SELECT max(id) FROM prompt_template_versions),1;
EOF
  "$PSQL" $PGH -d "$DBNAME" -v ON_ERROR_STOP=1 -tA -f "$WORKDIR/seed.sql" >/dev/null \
    || die "造数据失败，请单独执行 $WORKDIR/seed.sql 查看详情"

  echo "== 5/5 就绪 =="
  echo "组 A（已绑定）key: $API_KEY_VALUE"
  echo "组 B（未绑定）key: sk-inject-e2e-0002"
  echo "日志: $LOG"
}

cmd_test() {
  echo "== 组 A（绑定版本，期望出现标记） =="
  curl -s --max-time 180 -o "$WORKDIR/a.json" -w "http=%{http_code}\n" \
    -X POST "http://127.0.0.1:$PORT/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY_VALUE" -H "Content-Type: application/json" \
    -d "{\"model\":\"$TEST_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],\"max_completion_tokens\":120}"
  cat "$WORKDIR/a.json"; echo

  echo "== 组 B（未绑定，期望无标记） =="
  curl -s --max-time 180 -o "$WORKDIR/b.json" -w "http=%{http_code}\n" \
    -X POST "http://127.0.0.1:$PORT/v1/chat/completions" \
    -H "Authorization: Bearer sk-inject-e2e-0002" -H "Content-Type: application/json" \
    -d "{\"model\":\"$TEST_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],\"max_completion_tokens\":120}"
  cat "$WORKDIR/b.json"; echo

  echo "== 标记检测 =="
  grep -q INJECTION_MARKER_E2E "$WORKDIR/a.json" && echo "组 A: 含标记" || echo "组 A: 不含标记"
  grep -q INJECTION_MARKER_E2E "$WORKDIR/b.json" && echo "组 B: 含标记" || echo "组 B: 不含标记"

  echo "== 遥测（权威判据） =="
  "$PSQL" $PGH -d "$DBNAME" -c \
    "select id,attempt_no,group_id,outbound_profile,applied,reason,added_bytes,version_id from prompt_request_events order by id;"
  echo "== 诊断日志（若代码里保留了 PROMPT_DIAG 打印） =="
  grep PROMPT_DIAG "$LOG" 2>/dev/null | tail -10 || echo "（无诊断输出：说明请求未走到注入点）"
}

cmd_down() {
  echo "== 清理 =="
  taskkill //F //IM "$(basename "$BIN")" 2>/dev/null | head -1 || true
  sleep 2
  "$PSQL" $PGH -tAc "DROP DATABASE IF EXISTS $DBNAME;" >/dev/null 2>&1
  rm -rf "$WORKDIR" "$BIN" "$LOG"
  echo -n "残留端口: "; netstat -ano 2>/dev/null | grep -cE ":$PORT\s+.*LISTENING"
  echo -n "残留库: "
  "$PSQL" $PGH -tAc "select count(*) from pg_database where datname='$DBNAME';" 2>/dev/null | head -1
}

case "${1:-}" in
  up) cmd_up ;;
  test) cmd_test ;;
  down) cmd_down ;;
  *) echo "用法: $0 {up|test|down}"; exit 2 ;;
esac
