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
# 前置条件（脚本会自检，不满足直接以退出码 2 中止）：
#   探针必须在干净树上**可判别**。任一列在未注入时就是 RED，它对每条注入都会报
#   "caught"，这一列就不携带任何信息。本仓库的 Go 缓存必须落在工作区内：
#     export GOMODCACHE="$PWD/tmp/modcache" GOCACHE="$PWD/tmp/gocache" GOSUMDB=off
#
# 退出码：
#   0  每一条注入都已施加、都仍可编译，且都被门禁抓到。
#      L1_ONLY=1 时，标记为 L1 豁免的行按已评审结论计（不算漏抓）。
#   1  有注入已施加但门禁没抓到 —— 该保护缺少覆盖。
#   2  有注入因锚点失配而没能施加（该保护根本没被验证）、注入后无法编译（注入文本
#      本身残缺）、探针在干净树上不可判别，或某一行宣称由 L1 抓住但 L1 实际是绿的
#      而表里没有对应豁免；与 1 同时出现时报 2。
#      这些非 0 都不能降级成警告：1 是缺测试，2 是这份改坏验证本身不可信了。
#
# 说明：
#   * 只改源码文本，改完立即从备份还原；不使用 git checkout，避免影响未提交的改动。
#   * 编译失败不算"被抓到"。脚本在注入后先做一次编译自检：编译不过说明注入文本
#     本身是残缺的（锚点被截断、替换写坏），这一行会被判成 INVALID 并以退出码 2
#     中止，而不是伪装成 caught。历史上真的发生过：注入表的字段分隔符与注入文本里
#     的 `||` 冲突，`read` 把锚点截断，注入出去的是语法垃圾，"L0+L1 都红"其实只是
#     编译失败。
#   * 必须独占运行：脚本期间源码处于被改坏状态，不能与其他构建/测试并发。
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2
ROOT="$(pwd)"
FILTER="${1:-}"
DATASET="${DATASET:-./eval/datasets/synthetic-operations-v5.json}"
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

# 探针。抽成函数是为了让"干净树判别力自检"与矩阵走完全相同的命令——自检用另一条
# 命令时，它证明不了矩阵里的这一列。
#
# L0 必须用显式包前缀，不能用 ./...：本工作区把 Go module cache 放在仓库内的
# tmp/modcache，./... 会递归进去并因"不属于 go.work 列出的模块"而**永远**报错，
# 于是 l0 恒为 RED、每一行都会被判成 caught，这一列就失去了证据价值。
probe_l0() { (cd "$ROOT" && go test ./cmd/... ./internal/... ./eval/... >"$1" 2>&1); }
probe_l1() {
  (cd "$ROOT" && go run ./cmd/eval -dataset "$DATASET" -report "$2" \
    -code-version mutation -config "$CONFIG" >"$1" 2>&1)
}
# 注入后必须仍然编译。编译不过是弱信号：它不证明任何保护被覆盖，只证明注入写坏了。
# 用 -o 指向临时目录：cmd/eval 的默认输出名与 eval/ 目录同名，直接 go build 会报错。
probe_build() {
  mkdir -p "$1"
  (cd "$ROOT" && go build -o "$1/" ./cmd/... ./internal/... ./eval/... >"$2" 2>&1)
}

# 字段分隔符刻意不用 `|`：注入文本本身会出现 `||`（见 terminal-uniqueness-off 的
# 条件表达式），`read` 会从左边截断字段，注入出去的是残缺代码。用 ASCII US（\x1f），
# 它不会出现在 Go 源码或这些 shell 文本里。
SEP=$'\x1f'

# name <SEP> file <SEP> original text <SEP> injected text <SEP> L1 期望
#
# L1 期望（第 5 字段，省略即 required）：
#   required          = 这条保护必须由 L1（验收数据集）自己抓住。
#   waived:<claim-id> = 已评审豁免：该保护在 L1 结构性不可达，理由与代码证据见
#                       eval/coverage.json 的同名 claim。此时期望 L1 保持 green；
#                       若它反而变红，说明豁免已过期，需要同步 coverage.json。
# 豁免必须与 eval/coverage.json 的 claim 一一对应，不要在这里发明新豁免。
MUTATIONS=(
  "policy-allowlist${SEP}internal/domain/agent/policy.go${SEP}if !contains(worker.AllowedTools, toolID) {${SEP}if false \&\& !contains(worker.AllowedTools, toolID) {${SEP}waived:worker-tool-allow-list"
  "approval-bypass${SEP}internal/domain/agent/policy.go${SEP}ApprovalRequired: tool.Risk == string(RiskSideEffect) || tool.RequiresApproval,${SEP}ApprovalRequired: false,"
  "final-guard-off${SEP}internal/application/run/service.go${SEP}func guardToolResult(result string) error {${SEP}func guardToolResult(result string) error {\n\treturn nil"
  "terminal-uniqueness-off${SEP}internal/infrastructure/eventbus/memory.go${SEP}if item.Type == Completed || item.Type == Failed || item.Type == Canceled {${SEP}if false \&\& (item.Type == Completed || item.Type == Failed || item.Type == Canceled) {${SEP}waived:terminal-uniqueness-per-run"
  "cancel-signal-lost${SEP}internal/application/run/service.go${SEP}control.cancel()${SEP}_ = control.cancel"
  "idempotency-dedupe-off${SEP}internal/infrastructure/examplebusiness/fake_runtime.go${SEP}if result, ok := r.outreachByKey[idempotencyKey]; ok {${SEP}if result, ok := r.outreachByKey[idempotencyKey]; ok \&\& false {${SEP}waived:idempotent-replay-after-interruption"
  # 动作级审批的判定注入（spec 004 SC-002）：把审批通告的绑定从"即将执行的那一步"
  # 改回计划级（固定绑 steps[0]）。这正是旧实现的行为，也是 spec §1 记录的原始症状——
  # 审批事件通告的动作与真正执行的动作不是同一个。修复后混合计划用例必须变红。
  "approval-plan-level-binding${SEP}internal/application/run/service.go${SEP}\"step_id\":     step.StepID,${SEP}\"step_id\":     plan.Steps[0].StepID,"
)

# 判别力自检：先在不注入的干净树上跑一遍探针。任一列为 RED，它对每条注入都会报
# caught —— 那正是本项目记录过的 HIGH 缺陷（L0 探针曾因 tmp/modcache 恒为 RED，
# 整列不携带信息）。不先自检的话，环境问题会被读成"全部抓到"。
sanity_failed=0
if [ "$L1_ONLY" != "1" ]; then
  if probe_l0 "$work/sanity-l0.log"; then
    echo "probe sanity: L0 green on the clean tree (discriminative)"
  else
    echo "probe sanity: L0 is RED on a CLEAN tree — every row would be reported as caught"
    sanity_failed=1
  fi
fi
if probe_l1 "$work/sanity-l1.log" "$work/sanity-report.json"; then
  echo "probe sanity: L1 green on the clean tree (discriminative)"
else
  echo "probe sanity: L1 is RED on a CLEAN tree — every row would be reported as caught"
  sanity_failed=1
fi
if [ "$sanity_failed" != "0" ]; then
  cat <<'MSG'

FAIL: 探针在干净树上就失败，这次改坏验证无法区分"门禁抓到"和"环境坏了"，因此不施加任何注入。
      本仓库的常见原因：Go 构建缓存不在工作区内（例如只读的 /opt/gocache）。先执行：
        export GOMODCACHE="$PWD/tmp/modcache" GOCACHE="$PWD/tmp/gocache" GOSUMDB=off
      然后重跑。探针输出尾部：
MSG
  tail -n 4 "$work/sanity-l0.log" 2>/dev/null
  tail -n 4 "$work/sanity-l1.log" 2>/dev/null
  exit 2
fi
echo

printf '%-26s %-8s %-8s %s\n' "mutation" "L0" "L1" "result"
printf '%-26s %-8s %-8s %s\n' "--------------------------" "------" "------" "------"

undetected=0
unapplied=0
invalid=0
l1_expected_but_green=""
total=0
for entry in "${MUTATIONS[@]}"; do
  IFS="$SEP" read -r name file old new l1expect <<<"$entry"
  [ -n "$l1expect" ] || l1expect="required"
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
def unescape(value):
    # 与 eval/check-residual.py 的 unescape 保持一致。少解一种转义就是注入残缺：
    # `\t` 曾经没被解码，final-guard-off 注进去的是 Go 源里的字面反斜杠-t，
    # 编译失败被当成"门禁抓到"。
    return value.replace('\\n', '\n').replace('\\t', '\t').replace('\\&', '&')
path, old, new = sys.argv[1], unescape(sys.argv[2]), unescape(sys.argv[3])
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

  # 注入后先确认还能编译。编译不过说明注入文本残缺 —— 它会以"门禁变红"的形式
  # 混进 caught，而编译失败不证明任何保护被覆盖。
  if ! probe_build "$work/bin" "$work/build.log"; then
    printf '%-26s %-8s %-8s %s\n' "$name" "-" "-" "INVALID (注入后无法编译)"
    invalid=$((invalid + 1))
    cp "$backup" "$file"
    restore_current
    continue
  fi

  l0="skip"
  if [ "$L1_ONLY" != "1" ]; then
    if probe_l0 "$work/l0.log"; then l0="green"; else l0="RED"; fi
  fi

  l1="green"
  if probe_l1 "$work/l1.log" "$work/report.json"; then
    l1="green"
  else
    l1="RED"
  fi

  cp "$backup" "$file"
  restore_current

  if [ "$l0" = "red" ] || [ "$l0" = "RED" ] || [ "$l1" = "RED" ]; then
    verdict="caught"
    if [ "$l1expect" != "required" ] && [ "$l1" = "RED" ]; then
      verdict="caught (L1 豁免已过期)"
    fi
  elif [ "$l1expect" != "required" ] && [ "$L1_ONLY" = "1" ]; then
    # L1_ONLY 模式不跑 L0；豁免行保持 green 是预期结果，不是漏抓。
    verdict="waived (${l1expect#waived:})"
  else
    verdict="NOT CAUGHT"
    undetected=$((undetected + 1))
  fi

  # 表里声明由 L1 抓住、L1 却是绿的：这条保护只有 L0 兜底。必须报出来，否则
  # PROGRESS/README 里记录的 L0/L1 矩阵会继续和实测不符。
  if [ "$l1" != "RED" ] && [ "$l1expect" = "required" ] && [ "$verdict" = "caught" ]; then
    verdict="caught, L1 未抓住（表里无豁免）"
    l1_expected_but_green="$l1_expected_but_green $name"
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
if [ "$L1_ONLY" = "1" ]; then
  echo "note: L1_ONLY=1，L0 列未运行；waived 行表示该保护在 L1 结构性不可达（见 eval/coverage.json），不等于通过。"
fi
if [ "$unapplied" -gt 0 ]; then
  echo "FAIL: $unapplied of $total injections could not be applied (anchor not found)."
  echo "      Those protections were NOT verified: update the mutation table to the current source."
fi
if [ "$invalid" -gt 0 ]; then
  echo "FAIL: $invalid of $total injections did not compile after being applied."
  echo "      The injected text itself is broken (separator/anchor); that is not the gate catching a regression."
fi
if [ -n "$l1_expected_but_green" ]; then
  echo "FAIL: 这些注入只有 L0 抓住，表里没有 L1 豁免:$l1_expected_but_green"
  echo "      要么补 L1 覆盖，要么按 eval/coverage.json 的既有格式评审后标成 waived:<claim-id>；"
  echo "      同时同步 PROGRESS.md / README 里记录的 L0/L1 矩阵，别让文档继续声称 L1 会抓住它。"
fi
if [ "$undetected" -gt 0 ]; then
  echo "FAIL: $undetected of $total injected faults passed the gate undetected."
  echo "      A green gate that cannot fail proves nothing; add coverage for the rows above."
fi
if [ "$unapplied" -gt 0 ] || [ "$invalid" -gt 0 ] || [ -n "$l1_expected_but_green" ]; then
  # 2 优先于 1：注入没施加成功、或注入本身残缺、或表里的 L1 期望与实测不符时，
  # 这一行的"抓到/没抓到"无从谈起，结论不可信。
  exit 2
fi
if [ "$undetected" -gt 0 ]; then
  exit 1
fi
echo "PASS: all $total injected faults were caught by the gate."
exit 0
