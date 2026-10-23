#!/usr/bin/env python3
"""QT-09 ③ 契约一致性的跨仓比对入口（core 侧工具）。

同一份语料（tellurion-contracts:conformance/cases.json）分别喂给 core 与 agent
的实现，任一侧语义漂移即红。本脚本只负责「按侧点名跑、如实汇总、按结果给退出码」：

  - 某侧测试失败               -> 打印该侧输出，最终 exit 1
  - 某侧目录不存在（未 checkout）-> [skip] + GitHub ::warning，不算通过，最终 exit 0
                                   但摘要显式写「N 侧跳过（未 checkout）」
  - 语料读不到/不合法           -> ::warning 并如实写「语料未知」（各侧自己会 fail-loud）

语料内容不在这里复制或改写：唯一真相是契约仓里的 cases.json。

只用标准库（Python 3.9+），不引入第三方依赖。
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
from pathlib import Path

# 本文件位于 <core>/tools/，所以仓库根是 parents[1]。
REPO_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_WORKSPACE = REPO_ROOT.parent

# PATH 里没有 go 时的回退路径（本机 Windows 的 go1.27 安装点，与任务书一致）。
FALLBACK_GO = Path(r"C:\Users\ruoxi\.local\lib\go\bin\go.exe")

# 侧名、仓目录、包路径。包路径用 forward slash：go 工具链两种分隔符都认。
SIDES = (
    ("core", "tellurion", "./pkg/conformance"),
    ("agent", "tellurion-agent", "./internal/conformance"),
)

CORPUS_REL = Path("tellurion-contracts") / "conformance" / "cases.json"
TEST_NAME = "TestContractConformance"
TIMEOUT_SECONDS = 900


def resolve_go(explicit: str | None) -> str:
    """解析 go 可执行文件；显式指定优先，其次 PATH，最后回退路径。"""
    if explicit:
        candidate = Path(explicit)
        if not candidate.is_file():
            sys.exit(f"[error] --go 指向的文件不存在: {candidate}")
        return str(candidate)

    found = shutil.which("go")
    if found:
        return found
    if FALLBACK_GO.is_file():
        return str(FALLBACK_GO)
    sys.exit(
        "[error] 找不到 go 可执行文件：PATH 里没有，回退路径 "
        f"{FALLBACK_GO} 也不存在。请用 --go 显式指定。"
    )


def describe_corpus(workspace: Path) -> str:
    """只读语料条数，不复制内容。返回给人读的摘要文本。"""
    path = workspace / CORPUS_REL
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return f"语料未知（{path} 不存在，契约仓未 checkout）"
    except (OSError, json.JSONDecodeError) as exc:
        return f"语料未知（{path} 读取/解析失败: {exc}）"

    if not isinstance(data, list) or not data:
        return f"语料未知（{path} 不是非空数组）"

    kinds = sorted({str(item.get("kind", "")) for item in data if isinstance(item, dict)})
    return f"语料 {len(data)} 条 / {len(kinds)} 个 kind（{path.as_posix()}）"


def run_side(go: str, side: str, repo: str, package: str, workspace: Path) -> str:
    """跑一侧；返回 "pass" / "fail" / "skip"。"""
    repo_dir = workspace / repo
    if not repo_dir.is_dir():
        print(f"[skip] {side}：{repo_dir} 未 checkout，本侧未执行")
        print(
            f"::warning title=契约一致性（{side}）未执行::工作区缺少 {repo_dir}，"
            "本侧没有跑过 —— 这不是通过，只是未 checkout。"
        )
        return "skip"

    cmd = [go, "test", "-count=1", "-run", TEST_NAME, package]
    print(f"[run ] {side}：在 {repo_dir} 执行 {' '.join(cmd)}")
    try:
        proc = subprocess.run(
            cmd,
            cwd=str(repo_dir),
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=TIMEOUT_SECONDS,
        )
    except subprocess.TimeoutExpired:
        print(f"[fail] {side}：超过 {TIMEOUT_SECONDS}s 未完成")
        return "fail"
    except OSError as exc:
        print(f"[fail] {side}：无法启动 go: {exc}")
        return "fail"

    if proc.stdout:
        print(proc.stdout.rstrip())
    if proc.stderr:
        print(proc.stderr.rstrip(), file=sys.stderr)

    if proc.returncode != 0:
        print(f"[fail] {side}：go test 退出码 {proc.returncode}")
        return "fail"

    print(f"[ ok ] {side}：通过")
    return "pass"


def main() -> int:
    if hasattr(sys.stdout, "reconfigure"):
        # CI 日志与本地 PowerShell 都按 UTF-8 读；避免 cp936 把中文测试输出打断。
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")

    parser = argparse.ArgumentParser(description="契约一致性跨仓比对（QT-09 ③）")
    parser.add_argument(
        "--workspace",
        default=str(DEFAULT_WORKSPACE),
        help="三仓所在的工作区目录（默认：core 仓的父目录）",
    )
    parser.add_argument(
        "--go",
        default=None,
        help="go 可执行文件路径（默认：PATH 里的 go，其次本机回退路径）",
    )
    args = parser.parse_args()

    workspace = Path(args.workspace).resolve()
    if not workspace.is_dir():
        print(f"[error] --workspace 不是目录: {workspace}", file=sys.stderr)
        return 1

    go = resolve_go(args.go)
    version = subprocess.run(
        [go, "version"], capture_output=True, text=True, encoding="utf-8", errors="replace"
    )
    if version.returncode != 0:
        print(f"[error] {go} version 执行失败: {version.stderr.strip()}", file=sys.stderr)
        return 1
    print(f"go: {version.stdout.strip()}（{go}）")
    print(f"workspace: {workspace}")
    print(f"kernel: {describe_corpus(workspace)}")

    results = {side: run_side(go, side, repo, package, workspace) for side, repo, package in SIDES}

    passed = [s for s, r in results.items() if r == "pass"]
    failed = [s for s, r in results.items() if r == "fail"]
    skipped = [s for s, r in results.items() if r == "skip"]

    print("=" * 64)
    print("契约一致性（QT-09 ③）摘要")
    print(f"  {describe_corpus(workspace)}")
    for side, _, _ in SIDES:
        label = {"pass": "通过", "fail": "失败", "skip": "跳过（未 checkout）"}[results[side]]
        print(f"  {side:<5}: {label}")
    print(
        f"  合计：通过 {len(passed)} / 失败 {len(failed)} / 跳过 {len(skipped)}"
        + (f" —— {len(skipped)} 侧跳过（未 checkout，不等于通过）" if skipped else "")
    )
    print("=" * 64)

    if failed:
        print(f"❌ 契约一致性红：{', '.join(failed)} 侧语义与语料不符", file=sys.stderr)
        return 1
    if not passed:
        print("⚠️ 没有任何一侧真正执行（全部未 checkout）—— 本次结论不构成通过", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
