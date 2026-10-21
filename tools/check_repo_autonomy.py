#!/usr/bin/env python3
"""QT-08：十二仓自治自检 —— 校验每条 repo autonomy 不变量（R1~R6）。

对应 ADR-D29 §2 的 R1~R5 + R6（功能边界有契约）。判据本身是这套拓扑的**唯一机械执法者**：
没有它，「每个仓都能独立自证」只写在文档里、没人核。

用法（在 core 仓根跑；兄弟仓以 `../tellurion-<x>` 同级 checkout 才能全量核）：
    python tools/check_repo_autonomy.py                 # 静态结构自检（快、确定、CI 安全）
    python tools/check_repo_autonomy.py --self-test     # 护栏自证（CI 每次都跑，理由见下）
    python tools/check_repo_autonomy.py --run-builds    # 额外逐仓跑 build/vet/test（⑤ 字面项，慢）

退出码：
    0 = 全部通过（含**已登记**的已知缺口）；1 = 有未登记的 RED（CI 据此失败）。

`--self-test` 为什么必须有：本脚本是 R1~R6 唯一的机械执法者，而它自己没人核 —— 护栏静默
失灵（漏检 `replace ( ... )` 块形式、把合规的本机 go.work 判成红）比没有护栏更糟，因为前者
最终会让人把它关掉。夹具建在临时目录，不碰任何真仓；无 git 时**显式 SKIP**而不是假通过。

六条不变量（本脚本怎么落地）：
  R1 单仓可构建   —— go.mod 里任何把模块指向**仓外文件系统路径**的 `=>` 即红；committed
                       `go.work` 即红。唯一容忍：agent 的 `replace tellurion => ../tellurion`
                       —— 它是 S0~S3 编译连体的**已知缺口**，必须登记到 QT-07 才不红（不是静默放行）。
                       core / contracts / kit 任何 `=>` 都该红（ADR-D31：S1 之后无子 module）。
  R2 单仓可自证   —— 逐仓按类型分派：go 仓要有自证 CI（build/vet/test）；ui 要 CI + web/package.json；
                       docs 要 CI + 链接检查脚本。**占位仓「有 CI 但无测试文件」显式列出，不静默通过**（⑤）。
  R3 有行为入口   —— AGENTS.md 要有「依赖」章节标题 + 「禁止」标记。缺则以**已登记缺口**方式放行（core 见下）。
  R4 依赖只认版本 —— 跨仓依赖必须是版本号（tag / 伪版本），不得是源码路径。本脚本不为其单列
                       检查项：源码路径形态已由 R1（仓外 `=>` 即红）拦下，版本一致性由 R5 汇总核，
                       两条合起来就是 R4；单列一个空函数是假装覆盖，不做。
  R5 契约是版本依赖 —— **core 汇总跑**：各仓 AGENTS「依赖的契约版本」表里对 contracts/kit 的声明
                       必须彼此一致（今天都是「待 S2 → v0.1.0」；一旦有人先钉 v0.1.0、有人钉 v0.2.0 即分叉 → 红）。
  R6 功能边界有契约 —— 每仓 docs/{db-schema,api-schema,dataflow}.md 存在且非空。
                       **core 与 tellurion-docs 豁免**（如实登记，不假装）：core 的表/接口/链路真相
                       直接承载于 SPEC.md / ARCHITECTURE.md（见 REPO-MAP「core 与本仓豁免」），
                       docs 仓无表无端点、是知识库不是服务。

已知缺口登记表（KNOWN_GAPS）是**单一、可审计**的来源：每条 (仓, 检查) → 工单号，脚本会去
对应的 TASKS.md 核实那个工单号**确实存在**——「登记了才放行，没登记就是红」。这样既满足「未通过项
必须在台账有单号」，又不会因为脚本里写死一句「算了」就把缺口悄悄吞掉（那是 QT-61 要消灭的噪声）。

注意 --run-builds 会真的编译兄弟仓（含 agent 经 replace 连体编译 core），慢且吃 CPU；默认不开。
"""

from __future__ import annotations

import argparse
import re
import shutil
import subprocess
import tempfile
from pathlib import Path

# 规范仓清单（唯一名字源见 docs/design/qt-decoupling/repo-inventory.md，这里按类型标注）。
# type: go | node | docs
REPOS: list[tuple[str, str]] = [
    ("tellurion", "go"),
    ("tellurion-contracts", "go"),
    ("tellurion-kit", "go"),
    ("tellurion-agent", "go"),
    ("tellurion-data", "go"),
    ("tellurion-strategy", "go"),
    ("tellurion-risk", "go"),
    ("tellurion-analytics", "go"),
    ("tellurion-orchestration", "go"),
    ("tellurion-broker", "go"),
    ("tellurion-ui", "node"),
    ("tellurion-docs", "docs"),
]

# R6 豁免（真相承载方式不同，非遗漏）。理由写进输出，不留「静默跳过」。
R6_EXEMPT = {
    "tellurion": "core：表/接口/链路真相直接承载于 SPEC.md / ARCHITECTURE.md（REPO-MAP 记 core 豁免）",
    "tellurion-docs": "docs 仓：总体设计知识库，无自有表与端点",
}

# 已知缺口：(仓, 检查) → (工单号, 该仓台账里应出现的字符串)。脚本核实工单号在台账在册。
KNOWN_GAPS = {
    ("tellurion-agent", "R1"): ("QT-07", "QT-07"),
    ("tellurion", "R3"): ("QT-08", "QT-08"),
}

BOUNDARY_FILES = ("docs/db-schema.md", "docs/api-schema.md", "docs/dataflow.md")
# 依赖版本表里对 contracts/kit 的声明取样用。
CONTRACT_PINS = ("tellurion-contracts", "tellurion-kit")
# R5 取样：只提版本 token；带「待定/待 Sx」措辞的格子算**尚未钉**，不算钉版。
VERSION_RE = re.compile(r"v\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?")
PENDING_RE = re.compile(r"待定|待\s*S\d|尚未")


def core_dir() -> Path:
    return Path(__file__).resolve().parent.parent


def resolve_repo(name: str) -> Path:
    """core 用脚本所在仓；其余按同级目录 `../<name>` 解析。"""
    cd = core_dir()
    if name == cd.name:
        return cd
    return (cd.parent / name).resolve()


def read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return ""


def task_ticket_registered(repo_dir: Path, needle: str) -> bool:
    """该仓 TASKS（或 core TASKS）里是否出现了这个工单号 —— 缺口「登记」的机械判据。"""
    for cand in (repo_dir / "docs" / "TASKS.md", core_dir() / "docs" / "TASKS.md"):
        if needle in read(cand):
            return True
    return False


# ───────────────────────── R1：仓外 replace / committed go.work ─────────────────────────

REPLACE_ARROW_RE = re.compile(r"=>\s*(\S+)")


def check_r1(repo: str, d: Path) -> list[tuple[str, str]]:
    """返回违规列表 [(detail, ticket_needle_or_None)]。None ticket = 未登记 → 红。"""
    gomod = d / "go.mod"
    if not gomod.exists():
        return []  # 非 go 仓，N/A
    issues: list[tuple[str, str]] = []
    # 取任何含 `=>` 的非注释行（兼容单行 `replace X => Y` 与块形式 `replace ( X => Y )`）。
    # 注意：不能只 grep `^replace` —— 块形式里的行不以 replace 开头，那样会漏检（假护栏）。
    for raw in read(gomod).splitlines():
        line = raw.strip()
        if line.startswith("//") or "=>" not in line:
            continue
        m = REPLACE_ARROW_RE.search(line)
        if not m:
            continue
        target = m.group(1)
        # 指向仓外文件系统路径：以 ../ 、/ 、盘符绝对路径、或 `.` 开头。
        escapes = (target.startswith("../") or target.startswith("/")
                   or target == "." or re.match(r"^[A-Za-z]:[\\/]", target))
        if escapes:
            key = (repo, "R1")
            ticket = KNOWN_GAPS[key][1] if key in KNOWN_GAPS else None
            issues.append((f"{repo}: replace 指向仓外 `{target}`", ticket))
    # committed go.work / go.work.sum：**入库**即红（不是「存在」即红）。
    # 各仓 AGENTS 明写「本机同时改多个仓时可用 go.work 联调，但它不入库（.gitignore 已挡）」，
    # 所以判 exists 会把合规的本机联调状态误判成红 —— 误伤的护栏会让人整段删掉它。
    for rel in ("go.work", "go.work.sum"):
        if git_tracked(d, rel):
            issues.append((f"{repo}: {rel} 已入库（应 gitignore，只作本机联调产物）", None))
    return issues


def git_tracked(d: Path, rel: str) -> bool:
    """`rel` 是否进了 git index（tracked）。文件不存在 / 被 gitignore / 无 git 一律 False。"""
    if shutil.which("git") is None:
        return False
    r = subprocess.run(["git", "ls-files", "--error-unmatch", rel],
                       cwd=d, capture_output=True, text=True)
    return r.returncode == 0


# ───────────────────────── R3：AGENTS 行为入口两节 ─────────────────────────

def check_r3(repo: str, d: Path) -> list[tuple[str, str]]:
    agents = d / "AGENTS.md"
    if not agents.exists():
        return [(f"{repo}: 缺 AGENTS.md", None)]
    text = read(agents)
    issues: list[tuple[str, str]] = []
    if not re.search(r"^#{2,4}[ \t].*依赖", text, re.M):
        key = (repo, "R3")
        ticket = KNOWN_GAPS[key][1] if key in KNOWN_GAPS else None
        issues.append((f"{repo}: AGENTS 无「依赖…」章节标题（契约版本无处声明）", ticket))
    if "禁止" not in text:
        issues.append((f"{repo}: AGENTS 无「禁止」标记", None))
    return issues


# ───────────────────────── R5：跨仓契约版本一致（core 汇总） ─────────────────────────

def collect_contract_pin(repo: str, d: Path) -> dict[str, str]:
    """从 AGENTS「依赖的契约版本」表里抓 contracts/kit 后紧跟的版本 token。"""
    agents = d / "AGENTS.md"
    if not agents.exists():
        return {}
    text = read(agents)
    out: dict[str, str] = {}
    for name in CONTRACT_PINS:
        # 表格行：| `tellurion-contracts` | <版本> | ... |
        m = re.search(rf"\|\s*`?{re.escape(name)}`?\s*\|\s*([^|]+?)\s*\|", text)
        if m:
            out[name] = m.group(1).strip()
    return out


def check_r5(present: dict[str, Path]) -> list[str]:
    """按 pin 名归组，跨仓**已钉版本**不一致 → 红；钉/待定混布 → 显式点名（非静默、也非硬红）。

    取样时只提版本 token，不按整格字符串比对：各仓写法天然是
    「待定（S2 后 `v0.1.0`）」vs「`v0.1.0`」，整格比对会把「S2 半收口」这个
    本脚本自己认定为允许的状态判成红（假红比没红更糟：它会在最该看的时候被关掉）。
    """
    by_pin: dict[str, dict[str, str]] = {}
    for repo, d in present.items():
        for pin, cell in collect_contract_pin(repo, d).items():
            by_pin.setdefault(pin, {})[repo] = cell

    out: list[str] = []
    for pin, cells in sorted(by_pin.items()):
        pinned: set[str] = set()
        planned: set[str] = set()
        unpinned: int = 0
        for cell in cells.values():
            ver = VERSION_RE.search(cell)
            if PENDING_RE.search(cell):
                if ver:
                    planned.add(ver.group(0))
                else:
                    unpinned += 1
            elif ver:
                pinned.add(ver.group(0))
        if len(pinned) > 1:
            out.append(f"R5：{pin} 在各仓**已钉**的版本不一致 → {sorted(pinned)}")
            continue
        if pinned and (planned or unpinned):
            out.append(f"R5-note：{pin} 已出现钉版 {sorted(pinned)}，但仍有 {len(planned) + unpinned} 个仓标「待定」"
                       f"（计划版 {sorted(planned) if planned else '无版本号'}；S2/QT-07 收口前允许，须一致收敛）")
        elif not pinned and len(planned) > 1:
            out.append(f"R5-note：{pin} 各仓都还没钉，但计划版本已分叉 → {sorted(planned)}（收口前先对齐口径）")
    return out


# ───────────────────────── R6：三份功能边界契约 ─────────────────────────

def check_r6(repo: str, d: Path) -> list[tuple[str, str]]:
    if repo in R6_EXEMPT:
        return []  # 豁免在输出里单列，不进 issues
    issues = []
    for rel in BOUNDARY_FILES:
        f = d / rel
        if not f.exists():
            issues.append((f"{repo}: 缺 {rel}（R6 边界契约）", None))
        elif f.stat().st_size == 0:
            issues.append((f"{repo}: {rel} 为空", None))
    return issues


# ───────────────────────── R2：按类型分派的自证入口 ─────────────────────────

def has_ci(d: Path) -> bool:
    return any((d / ".github" / "workflows").glob("*.yml")) or any((d / ".github" / "workflows").glob("*.yaml"))


def check_r2(repo: str, rtype: str, d: Path) -> tuple[list[str], list[str]]:
    """返回 (reds, notes)。notes = 显式列出、非静默的占位/豁免说明。"""
    reds: list[str] = []
    notes: list[str] = []
    if not has_ci(d):
        reds.append(f"R2：{repo} 无 .github/workflows（单仓不能自证 build/test）")
    if rtype == "go":
        tests = list(d.rglob("*_test.go"))
        if not tests:
            # 占位仓（S0）：有 CI 但无测试文件 —— 显式列出，不静默通过（⑤）。
            notes.append(f"R2：{repo} 无 `*_test.go`（S0 占位仓；CI 仍自证 build/vet）")
    elif rtype == "node":
        if not (d / "web" / "package.json").exists():
            reds.append(f"R2：{repo} 缺 web/package.json（node 构建入口不在）")
    elif rtype == "docs":
        if not (d / "tools" / "check_doc_links.py").exists():
            reds.append(f"R2：{repo} 缺 tools/check_doc_links.py（链接自证入口不在）")
    return reds, notes


# ───────────────────────── 可选：--run-builds 逐仓编译 ─────────────────────────

def run_builds(present: dict[str, tuple[str, Path]]) -> list[str]:
    reds = []
    for repo, (rtype, d) in sorted(present.items()):
        if rtype != "go":
            continue  # 只核 go 仓；ui/docs 的构建执行属其各自 CI（本脚本不代跑 npm/pytest）
        for step, cmd in (("build", ["go", "build", "./..."]), ("vet", ["go", "vet", "./..."])):
            r = subprocess.run(cmd, cwd=d, capture_output=True, text=True)
            if r.returncode != 0:
                reds.append(f"run-builds：{repo} `go {step}` 退出 {r.returncode}\n{r.stderr.strip()[:400]}")
                break
    return reds


def self_test() -> int:
    """护栏自证：造夹具喂给各检查函数，验它**拦得住真违规、也不误伤合规状态**。

    为什么非要有这条：本脚本是 R1~R6 的唯一机械执法者，它自己错了没人核 ——
    「假护栏比没护栏更糟」（QT-08 §③ / core AGENTS §8.5.3-2）。夹具全建在 tempfile 里，
    不碰任何真仓；`git` 缺席时**显式跳过**并说明，不假装通过。
    """
    print("Quant Lab — check_repo_autonomy 护栏自证（夹具在临时目录，不触真仓）")
    if shutil.which("git") is None:
        print("  [SKIP] 本机无 git ⇒ 无法造 tracked go.work 夹具 —— 这是跳过，不是通过")
        # 退出码仍为 0（无 git 是环境限制，判红是假红），但必须在 CI 里留下可见的痕迹：
        # 否则「自证跑过了」和「自证没跑」在线上长得一模一样 —— 正是 QT-18 那条降级空跑。
        print("::warning title=QT-08 护栏自证未运行::本机无 git，R1 的 go.work tracked 腿与"
              "入库 go.work 腿全部跳过（8 条腿里 5 条依赖 git 夹具）。这不代表护栏自证通过。")
        return 0

    fails: list[str] = []

    def expect(label: str, ok: bool) -> None:
        print(f"  [{'ok' if ok else 'FAIL'}] {label}")
        if not ok:
            fails.append(label)

    def r1_on(text: str) -> list[tuple[str, str]]:
        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp) / "tellurion-fake"
            d.mkdir()
            (d / "go.mod").write_text(text, encoding="utf-8")
            return check_r1("tellurion-fake", d)

    def r5_on(cells: dict[str, str]) -> list[str]:
        with tempfile.TemporaryDirectory() as tmp:
            present = {}
            for repo, cell in cells.items():
                d = Path(tmp) / repo
                (d / "docs").mkdir(parents=True)
                (d / "AGENTS.md").write_text(
                    "## 4. 依赖的契约版本\n\n| 依赖 | 版本 | 说明 |\n|---|---|---|\n"
                    f"| `tellurion-contracts` | {cell} | x |\n", encoding="utf-8")
                present[repo] = d
            return check_r5(present)

    # R1 正证据：单行与块形式两种仓外 replace 都得抓到（只 grep `^replace` 会漏块形式）。
    expect("R1 抓到单行仓外 replace",
           any("仓外" in i[0] for i in r1_on("module x\n\nreplace tellurion => ../tellurion\n")))
    expect("R1 抓到 replace 块形式里的仓外路径",
           any("仓外" in i[0] for i in r1_on("module x\n\nreplace (\n\ttellurion => ../tellurion\n)\n")))
    # R1 反证：把模块替成 fork（目标是**模块路径**、不是文件系统）不该红 —— 红了就等于
    # 把「不许源码路径依赖」写成「不许任何 replace」，那是会逼人关掉护栏的误伤。
    expect("R1 不误伤 fork 替换（目标是模块版本而非路径）",
           not r1_on("module x\n\nreplace tellurion v0.1.0 => github.com/some/fork v0.1.0\n"))

    # R1 go.work：AGENTS 允许本机联调（不入库），所以判据必须是 tracked 而不是 exists。
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "tellurion-fake"
        (d / "docs").mkdir(parents=True)
        (d / "go.mod").write_text("module x\n", encoding="utf-8")
        (d / "go.work").write_text("go 1.25\n", encoding="utf-8")
        subprocess.run(["git", "init", "-q"], cwd=d, check=True)
        subprocess.run(["git", "add", "go.mod"], cwd=d, check=True)
        subprocess.run(["git", "-c", "user.email=t@t", "-c", "user.name=t",
                        "commit", "-qm", "init"], cwd=d, check=True)
        expect("R1 不误伤未入库（哪怕没 .gitignore）的本机 go.work",
               not any("go.work" in i[0] for i in check_r1("tellurion-fake", d)))
        subprocess.run(["git", "add", "go.work"], cwd=d, check=True)
        expect("R1 抓到入库的 go.work",
               any("go.work" in i[0] for i in check_r1("tellurion-fake", d)))

    # R5：钉版一致 → 无话；两仓钉了不同版 → 红；「有的钉了、有的还待定」是 S2 半收口的
    # 允许状态 → 只点名不硬红（整格比对会把它误判成分叉，那种假红最容易被关掉）。
    expect("R5 各仓钉同一版 → 不红",
           not r5_on({"a": "`v0.1.0`", "b": "v0.1.0"}))
    expect("R5 两仓钉了不同版 → 红",
           any(x.startswith("R5：") for x in r5_on({"a": "v0.1.0", "b": "v0.2.0"})))
    expect("R5 钉版与「待定（S2 后 v0.1.0）」混布 → 点名但不硬红",
           all(x.startswith("R5-note：") for x in r5_on({"a": "v0.1.0", "b": "待定（S2 后 `v0.1.0`）"}))
           and bool(r5_on({"a": "v0.1.0", "b": "待定（S2 后 `v0.1.0`）"})))

    if fails:
        print(f"\n结果：护栏自证 RED —— {len(fails)} 条：{fails}")
        return 1
    print("\n结果：护栏自证 GREEN —— R1/R5 既能拦住真违规，也不误伤合规状态。")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="QT-08 十二仓自治自检（R1~R6）")
    ap.add_argument("--run-builds", action="store_true", help="逐 go 仓实跑 build/vet（慢；⑤ 字面执行项）")
    ap.add_argument("--self-test", action="store_true", help="护栏自证：夹具喂进检查函数，验它拦得住违规也不误伤合规态")
    args = ap.parse_args()

    if args.self_test:
        return self_test()

    core = core_dir()
    present: dict[str, Path] = {}
    absent: list[str] = []
    for name, _ in REPOS:
        d = resolve_repo(name)
        (present.__setitem__(name, d) if d.exists() else absent.append(name))

    reds: list[str] = []
    gaps_ok: list[str] = []   # 已登记缺口（放行，但要显式报出，不静默）
    exemptions: list[str] = []
    notes: list[str] = []

    type_of = dict(REPOS)

    for repo, d in sorted(present.items()):
        # R6 豁免显式列出
        if repo in R6_EXEMPT:
            exemptions.append(f"R6 豁免：{repo} —— {R6_EXEMPT[repo]}")

        for label, issues in (
            ("R1", check_r1(repo, d)),
            ("R3", check_r3(repo, d)),
            ("R6", check_r6(repo, d)),
        ):
            for detail, ticket in issues:
                if ticket is None:
                    reds.append(f"{label} 红（未登记）：{detail}")
                elif task_ticket_registered(d, ticket):
                    gaps_ok.append(f"{label} 已知缺口（登记 {ticket}）：{detail}")
                else:
                    reds.append(f"{label} 红（缺口登记号 {ticket} 在台账查无）：{detail}")

        r2_reds, r2_notes = check_r2(repo, type_of[repo], d)
        reds += r2_reds
        notes += r2_notes

    # R5 跨仓汇总（core 跑，比对各仓对 contracts/kit 的版本声明）。只算一次：
    # 分两遍调 check_r5 是在红/note 之间制造「两次结果可以不一致」的空间。
    r5 = check_r5(present)
    reds += [x for x in r5 if x.startswith("R5：")]
    notes += [x for x in r5 if x.startswith("R5-note：")]

    if args.run_builds:
        reds += run_builds({r: (type_of[r], d) for r, d in present.items()})

    # ── 报告 ──
    print("Quant Lab — repo autonomy self-check (R1~R6)\n")
    if absent:
        print(f"未在本机 checkout（跳过，需全量时在兄弟仓齐的机器上跑）：{', '.join(absent)}\n")

    for line in exemptions:
        print(f"  [豁免] {line}")
    for line in notes:
        print(f"  [显式] {line}")
    for line in gaps_ok:
        print(f"  [缺口登记] {line}")

    if reds:
        print("\n结果：RED —— 下列项未通过且无有效台账登记：")
        for line in reds:
            print(f"  ✗ {line}")
        print(f"\n扫描 {len(present)} 仓；红 {len(reds)} 项。")
        return 1

    print(f"\n结果：GREEN —— {len(present)} 仓全部满足 R1~R6（含 {len(gaps_ok)} 条已登记缺口、"
          f"{len(exemptions)} 条豁免，均显式列出，无静默忽略）。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
