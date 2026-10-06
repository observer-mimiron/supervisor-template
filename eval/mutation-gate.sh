#!/usr/bin/env bash
#
# mutation-gate.sh — 证明 L0/L1 门禁不是摆设。
#
# 对每一条受保护的行为，在真实源码里注入一个故障，然后跑门禁：
#   期望：门禁必须变红（非 0 退出）。
#   如果门禁仍然是绿的，说明这条保护没有被评测覆盖 —— 那才是真正的问题。
#
# 用法：
#   ./eval/mutation-gate.sh              # 跑全部
#   ./eval/mutation-gate.sh approval     # 只跑名字匹配的那条
#   L1_ONLY=1 ./eval/mutation-gate.sh    # 只跑 L1（跳过 go test，快）
#
# 退出码：
#   0  每一条注入都已施加，且都被门禁抓到。
#   1  有注入已施加但门禁没抓到 —— 该保护缺少覆盖。
#   2  有注入因锚点失配而没能施加（该保护根本没被验证），或工作区自检失败；
#      与 1 同时出现时报 2。
#      两种非 0 都不能降级成警告：1 是缺测试，2 是这份改坏验证本身过期了。
#
# 说明：
#   * 只改源码文本，改完立即从备份还原；不使用 git checkout，避免影响未提交的改动。
#   * 编译失败也算"被抓到"（门禁确实红了），但输出只有 RED + caught，不区分
#     "断言抓到"和"编译不过"。编译不过属于弱信号：它不证明这条保护有覆盖，
#     不要拿它当覆盖证据。
#   * 必须独占运行：脚本期间源码处于被改坏状态，不能与其他构建/测试并发。
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2
ROOT="$(pwd)"
FILTER="${1:-}"
DATASET="${DATASET:-./eval/datasets/synthetic-operations-v4.json}"
CONFIG="${CONFIG:-./config.example.toml}"
L1_ONLY="${L1_ONLY:-0}"

# 本脚本会临时改坏源码，因此必须独占工作区：与其他 go build / go test / 另一次
# 改坏验证并发执行时，对方会编译到被注入的代码，结论无效。
lock="$ROOT/.mutation-gate.lock"
if ! mkdir "$lock" 2>/dev/null; then
  echo "another mutation run is active ($lock); source files are being rewritten, retry after it finishes"
  exit 2
fi

work="$(mktemp -d)"

# 中断安全：注入生效期间被 Ctrl-C / CI 取消 / 超时打断时，必须把源码还原回去，
# 否则工作区会残留 `if false && ...` 这类改坏代码。
current_file=""
current_backup=""
restore_current() {
  if [ -n "$current_file" ] && [ -n "$current_backup" ] && [ -f "$current_backup" ]; then
    cp "$current_backup" "$current_file" 2>/dev/null || true
  fi
  current_file=""
  current_backup=""
}
on_exit() {
  restore_current
  rm -rf "$work"
  rmdir "$lock" 2>/dev/null || true
}
trap on_exit EXIT
trap 'on_exit; exit 130' INT
trap 'on_exit; exit 143' TERM

# name | file | original text | injected text
MUTATIONS=(
  'policy-allowlist|internal/domain/agent/policy.go|if !contains(worker.AllowedTools, toolID) {|if false \&\& !contains(worker.AllowedTools, toolID) {'
  'approval-bypass|internal/domain/agent/policy.go|ApprovalRequired: tool.Risk == string(RiskSideEffect) || tool.RequiresApproval,|ApprovalRequired: false,'
  'final-guard-off|internal/application/run/service.go|func guardToolResult(result string) error {|func guardToolResult(result string) error {\n\treturn nil'
  'terminal-uniqueness-off|internal/infrastructure/eventbus/memory.go|if item.Type == Completed || item.Type == Failed || item.Type == Canceled {|if false \&\& (item.Type == Completed || item.Type == Failed || item.Type == Canceled) {'
  'cancel-signal-lost|internal/application/run/service.go|control.cancel()|_ = control.cancel'
  'idempotency-dedupe-off|internal/infrastructure/examplebusiness/fake_runtime.go|if result, ok := r.outreachByKey[idempotencyKey]; ok {|if result, ok := r.outreachByKey[idempotencyKey]; ok \&\& false {'
  # 动作级审批的判定注入（spec 004 SC-002）：把审批通告的绑定从"即将执行的那一步"
  # 改回计划级（固定绑 steps[0]）。这正是旧实现的行为，也是 spec §1 记录的原始症状——
  # 审批事件通告的动作与真正执行的动作不是同一个。修复后混合计划用例必须变红。
  'approval-plan-level-binding|internal/application/run/service.go|"step_id":     step.StepID,|"step_id":     plan.Steps[0].StepID,'
)

printf '%-26s %-8s %-8s %s\n' "mutation" "L0" "L1" "result"
printf '%-26s %-8s %-8s %s\n' "--------------------------" "------" "------" "------"

undetected=0
unapplied=0
total=0
for entry in "${MUTATIONS[@]}"; do
  IFS='|' read -r name file old new <<<"$entry"
  if [ -n "$FILTER" ] && [[ "$name" != *"$FILTER"* ]]; then continue; fi
  total=$((total + 1))

  backup="$work/$(echo "$file" | tr '/' '_')"
  cp "$file" "$backup"
  # Record every applied mutation so the residual self-check compares the full
  # injected text rather than guessing from a single line.
  printf '%s\n' "$entry" >>"$work/applied.txt"
  current_file="$file"
  current_backup="$backup"

  # 注入：必须精确命中一次，否则说明源码变了，这条改坏验证需要更新
  if ! python3 - "$file" "$old" "$new" <<'PY'
import sys
path, old, new = sys.argv[1], sys.argv[2].replace('\\n', '\n').replace('\\&', '&'), sys.argv[3].replace('\\n', '\n').replace('\\&', '&')
text = open(path, encoding='utf-8').read()
count = text.count(old)
if count != 1:
    sys.exit(f'anchor matched {count} times in {path}: {old[:60]!r}')
open(path, 'w', encoding='utf-8').write(text.replace(old, new, 1))
PY
  then
    printf '%-26s %-8s %-8s %s\n' "$name" "-" "-" "SKIP (anchor not found)"
    # 锚点失配 = 这条保护没有被验证，既不是"抓到"也不是"通过"。必须计入失败，
    # 否则注入表与源码脱节之后，脚本仍会宣布"全部抓到"并以 0 退出。
    unapplied=$((unapplied + 1))
    cp "$backup" "$file"
    restore_current
    continue
  fi

  l0="skip"
  if [ "$L1_ONLY" != "1" ]; then
    # 必须用显式包前缀，不能用 ./...：本工作区把 Go module cache 放在仓库内的
    # tmp/modcache，./... 会递归进去并因"不属于 go.work 列出的模块"而**永远**报错，
    # 于是 l0 恒为 RED、每一行都会被判成 caught，这一列就失去了证据价值。
    if (cd "$ROOT" && go test ./cmd/... ./internal/... ./eval/... >"$work/l0.log" 2>&1); then l0="green"; else l0="RED"; fi
  fi

  l1="green"
  if (cd "$ROOT" && go run ./cmd/eval -dataset "$DATASET" -report "$work/report.json" \
        -code-version mutation -config "$CONFIG" >"$work/l1.log" 2>&1); then
    l1="green"
  else
    l1="RED"
  fi

  cp "$backup" "$file"
  restore_current

  if [ "$l0" = "red" ] || [ "$l0" = "RED" ] || [ "$l1" = "RED" ]; then
    verdict="caught"
  else
    verdict="NOT CAUGHT"
    undetected=$((undetected + 1))
  fi
  printf '%-26s %-8s %-8s %s\n' "$name" "$l0" "$l1" "$verdict"
done

# 自检：确认没有任何注入残留在工作区。必须用完整注入文本比对：
# 像 final-guard-off 这类"以原文为前缀"的注入，只比首行会产生假阳性。
if ! python3 "$ROOT/eval/check-residual.py" "$work/applied.txt"; then
  echo "FAIL: injected fault(s) left in the working tree; restore those files before continuing."
  exit 2
fi

echo
if [ "$unapplied" -gt 0 ]; then
  echo "FAIL: $unapplied of $total injections could not be applied (anchor not found)."
  echo "      Those protections were NOT verified: update the mutation table to the current source."
fi
if [ "$undetected" -gt 0 ]; then
  echo "FAIL: $undetected of $total injected faults passed the gate undetected."
  echo "      A green gate that cannot fail proves nothing; add coverage for the rows above."
fi
if [ "$unapplied" -gt 0 ]; then
  # 2 优先于 1：注入都没施加成功时，这一行的"抓到/没抓到"无从谈起，结论不可信。
  exit 2
fi
if [ "$undetected" -gt 0 ]; then
  exit 1
fi
echo "PASS: all $total injected faults were caught by the gate."
exit 0
