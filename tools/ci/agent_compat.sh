#!/bin/sh
# QT-18：拿**当前 core 工作区**编译一遍 agent —— 过渡期（S0~S3）编译连体护栏。
#
# 为什么是脚本而不是 Makefile 目标：本机（Windows + Git Bash）没有 make，而这条正是
# 「未配只读 PAT 时的降级门禁」（PR 模板 / docs/TASKS.md QT-18 都指向它）。写在 make 里
# 等于门禁在最常用的机器上跑不了。CI 的 agent-compat job 也调这同一份 —— 逻辑只有一处，
# 不会出现「CI 那份改了、本地那份忘了」的两份真相。
#
# 用法：
#   sh tools/ci/agent_compat.sh                  # 兄弟 agent 仓在 ../tellurion-agent
#   AGENT_REPO=/path/to/agent sh tools/...       # CI / 非同级 checkout
#   只 build/vet，不跑 agent 单测（那些要 core 二进制与 DB，成本不成比例；行为兼容属 QT-09）。
set -u

# core = 本脚本所在仓的根（不是 $PWD：从别处调用也要编当前这份 core）。
CORE=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
AGENT_REPO=${AGENT_REPO:-${CORE}/../tellurion-agent}

fail() {
    printf '::error::agent-compat：%s\n' "$1" >&2
    exit 1
}

command -v go >/dev/null 2>&1 || fail "go 不在 PATH（Windows 上通常是没导出 sdk 目录；先把它加进 PATH 再跑）"
[ -d "$AGENT_REPO" ] || fail "找不到兄弟 agent 仓 ${AGENT_REPO} —— 降级路径要求它 checkout 在 core 同级，或用 AGENT_REPO= 指定"

# 刻意 clone 到临时目录，不在 ../tellurion-agent 里原地改 go.mod：那会脏了正在编辑的 agent 工作区。
# 副作用（如实登记）：clone 只带 agent 的 HEAD，agent 侧**未提交**的改动不参与本次编译 ——
# 那是 CI 同款语义（CI 也是 checkout main 再 replace）。
tmpdir=$(mktemp -d 2>/dev/null) || fail "mktemp -d 失败"
trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM

git clone --quiet "$AGENT_REPO" "${tmpdir}/tellurion-agent" || fail "克隆 ${AGENT_REPO} 失败"

printf '  core:   %s\n  agent:  %s (HEAD)\n  → go build ./...\n' "$CORE" "$AGENT_REPO"
(
    cd "${tmpdir}/tellurion-agent" || exit 1
    go mod edit -replace tellurion="${CORE}"
    go build ./... && { printf '  → go vet ./...\n'; go vet ./...; }
)
ec=$?

if [ "$ec" -ne 0 ]; then
    printf '::error::agent-compat 红：当前 core 改动破坏了 agent 编译（退出 %s）。要么附 agent 侧适配 PR，要么在 .github/agent-compat-known-break 登记带单号的 known-break（QT-18 ③）后合入。\n' "$ec"
else
    printf '✅ agent-compat 绿：当前 core 改动不破 agent 编译\n'
fi
exit "$ec"
