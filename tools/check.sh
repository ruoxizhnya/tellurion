#!/usr/bin/env sh
# 本地提交前 / 推送前闸（D1 防复发入口）。
#
# 只报不修：不跑 `gofmt -w`，不自动提交，不改任何文件。
# 覆盖三条腿，与 CI 的 Build & Test job 同口径：
#   1) gofmt -l .   —— 非空即红（只列出文件，修不修由人决定）
#   2) go vet ./...
#   3) go test ./... -count=1
# 任一条失败即以该条退出码结束；三条都会跑完再汇总，不会因首条红而漏看后面。
#
# 用法：
#   sh tools/check.sh            # 判当前工作区（快，适合编辑中自查）
#   sh tools/check.sh --clean    # 判 HEAD 的干净 checkout（可推的绿，见 AGENTS §8.4）
#
# 背景：`gofmt -l .` 判的是工作区，CI 判的是提交出去的内容，两者可以不同
# （AGENTS.md §8.4 的 QT-01 教训）。推送决策请用 --clean。

set -u

mode="worktree"
case "${1:-}" in
  --clean) mode="clean" ;;
  "") ;;
  *)
    echo "用法: sh tools/check.sh [--clean]" >&2
    exit 2
    ;;
esac

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp=""
cleanup() {
  if [ -n "$tmp" ]; then
    git -C "$root" worktree remove --force "$tmp" >/dev/null 2>&1 || true
    rm -rf "$(dirname "$tmp")" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

work="$root"
if [ "$mode" = "clean" ]; then
  base="$(mktemp -d)"
  tmp="$base/wt"
  if ! git -C "$root" worktree add --detach "$tmp" HEAD >/dev/null; then
    echo "!! 干净 checkout 创建失败（HEAD $(git -C "$root" rev-parse --short HEAD)）" >&2
    exit 1
  fi
  work="$tmp"
  echo "==> 干净 checkout: HEAD $(git -C "$root" rev-parse --short HEAD)"
fi

cd "$work" || exit 1
fail=0

if ! command -v gofmt >/dev/null 2>&1; then
  echo "!! 找不到 gofmt（确认 Go 工具链在 PATH 里）" >&2
  exit 1
fi

echo "==> gofmt -l ."
unformatted="$(gofmt -l . 2>&1)"
if [ -n "$unformatted" ]; then
  echo "!! 以下文件未格式化（自行决定是否 gofmt -w；本脚本不改）："
  echo "$unformatted"
  fail=1
else
  echo "ok: 无未格式化文件"
fi

echo "==> go vet ./..."
go vet ./...
code=$?
if [ "$code" -ne 0 ]; then
  echo "!! go vet 失败（exit $code）"
  fail="$code"
fi

echo "==> go test ./... -count=1"
go test ./... -count=1
code=$?
if [ "$code" -ne 0 ]; then
  echo "!! go test 失败（exit $code）"
  fail="$code"
fi

if [ "$fail" -ne 0 ]; then
  echo "❌ check 未通过（exit $fail）"
  exit "$fail"
fi
echo "✅ check 通过"
