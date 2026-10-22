#!/usr/bin/env python3
"""QT-09 ① / QT-07 ⑤：契约 pin 必须是目标仓 `main` 的祖先。

为什么需要这条腿：ADR-D31（Plan B）之后 core / agent 用**伪版本**依赖
`tellurion-contracts` / `tellurion-kit`。伪版本可以指向任意 commit（含未评审的
旁支），而 `go build` 只关心「这个 commit 在不在」，不关心「它合没合进 main」。
QT-08 的 R5 只比对各仓 AGENTS 里的版本声明是否一致，核不到这一层 —— 本脚本补的
就是这一条。

判据（每个 pin 一条）：
  1. 从 `go.mod` 提取对 contracts/kit 的 `require` 版本；
  2. 伪版本取末尾 12 位 sha；tag 经 `git rev-parse` 解析到 commit；
  3. 该 commit 必须是目标仓 `origin/main`（本地无远程跟踪时退回 `main`）的祖先；
  4. 打印 `repo@sha → 提交日期`（日期取 committer date，不是伪版本里的时间戳）。
无法解析 / 拉不到目标仓 / **生效的** `replace` 覆盖了 pin —— 一律 RED（fail-closed），
不把「没核成」伪装成「核过了」。

目标仓从哪来：优先同级 checkout（`<workspace>/tellurion-contracts`），没有则匿名
clone `https://<module-path>` 到临时目录（core-only CI 走这条）。两仓当前 public；
若将来转私有，本步会 fail-loud，届时按 ADR-D31 的已知风险配 `GOPRIVATE` + 凭证。

用法：
    python tools/check_contract_pin.py                    # 扫 core + 同级所有 tellurion* go 仓
    python tools/check_contract_pin.py --workspace ..     # 显式 workspace
    python tools/check_contract_pin.py --repo tellurion-agent   # 只扫一个（可重复）
    python tools/check_contract_pin.py --offline          # 只许用本地 checkout（离线/自证）
    python tools/check_contract_pin.py --self-test        # 护栏自证（11 条夹具腿）

`--self-test` 为什么必须有：本脚本和 QT-08 的 R1/R5 一样是护栏，护栏自己没人核就会
静默失灵（例如把「祖先」写成「相等」、放行旁支 tag）。夹具造真 git 仓（main / 旁支 /
tag），证明它既拦得住旁支 pin，也不误伤 main 上的中段提交。
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

# 本工具守护的模块 → 目标仓目录名。新增受管契约模块时在这里登记一行。
CONTRACT_MODULES: dict[str, str] = {
    "github.com/ruoxizhnya/tellurion-contracts": "tellurion-contracts",
    "github.com/ruoxizhnya/tellurion-kit": "tellurion-kit",
}

# Go 伪版本：vX.Y.Z-<可选 pre-release>-yyyymmddhhmmss-<12 位 sha>[+incompatible]
PSEUDO_RE = re.compile(
    r"^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?-(\d{14})-([0-9a-f]{12})(?:\+incompatible)?$"
)
TAG_RE = re.compile(r"^v\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?$")


@dataclass(frozen=True)
class Pin:
    consumer: str
    module: str
    version: str


@dataclass(frozen=True)
class Replace:
    module: str
    version: str | None
    target: str


@dataclass(frozen=True)
class Target:
    name: str
    path: Path
    ref: str
    source: str  # local | remote


@dataclass(frozen=True)
class Verdict:
    ok: bool
    line: str


def core_dir() -> Path:
    return Path(__file__).resolve().parent.parent


def read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return ""


def git(
    repo: Path | None,
    *args: str,
    env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess:
    cmd = ["git"]
    if repo is not None:
        cmd += ["-C", str(repo)]
    cmd += list(args)
    e = os.environ.copy()
    # 永不发交互式凭据追问：父进程就算设了 =1 也压回 0；CI/无人值守下宁可 fail-loud，
    # 也不要挂到超时。调用方显式传入 env 时仍可覆盖（测试/调试用）。
    e["GIT_TERMINAL_PROMPT"] = "0"
    if env:
        e.update(env)
    return subprocess.run(
        cmd, capture_output=True, text=True, encoding="utf-8", errors="replace", env=e
    )


def last_line(text: str) -> str:
    lines = [ln.strip() for ln in (text or "").splitlines() if ln.strip()]
    return lines[-1] if lines else "(git 无输出)"


# ───────────────────────── go.mod 解析 ─────────────────────────

def _directive_body(line: str, keyword: str) -> tuple[str | None, bool]:
    """解析 `require` / `replace` 指令：返回 (指令体, 是否开启括号块)。

    用 split() 而不是 `startswith("require ")`：go.mod 的 token 分隔符可以是
    制表符或连续空格；写死一个空格会在手写/被编辑器改过的文件上漏掉整条
    require —— 那是假绿，比没有护栏更糟。
    """
    parts = line.split()
    if not parts or parts[0] != keyword:
        return None, False
    rest = parts[1:]
    if rest and rest[0] == "(":
        return "", True
    return " ".join(rest), False


def find_pins(consumer: str, text: str) -> list[Pin]:
    """提取受管模块的 require。兼容单行与 `require ( ... )` 块形式，忽略 // 注释。"""
    pins: list[Pin] = []
    in_block = False
    for raw in text.splitlines():
        line = raw.split("//", 1)[0].strip()
        if not line:
            continue
        if in_block:
            if line == ")":
                in_block = False
                continue
            body = line
        else:
            body, opens_block = _directive_body(line, "require")
            if opens_block:
                in_block = True
                continue
            if body is None:
                continue
        parts = body.split()
        if len(parts) >= 2 and parts[0] in CONTRACT_MODULES:
            pins.append(Pin(consumer, parts[0], parts[1]))
    return pins


def find_replaces(text: str) -> list[Replace]:
    """提取受管模块的 replace（等价于「require 里的版本不再是实际编译版本」）。"""
    out: list[Replace] = []
    in_block = False
    for raw in text.splitlines():
        line = raw.split("//", 1)[0].strip()
        if not line:
            continue
        if in_block:
            if line == ")":
                in_block = False
                continue
            body = line
        else:
            body, opens_block = _directive_body(line, "replace")
            if opens_block:
                in_block = True
                continue
            if body is None:
                continue
        if "=>" not in body:
            continue
        left, _, right = body.partition("=>")
        tokens = left.split()
        if not tokens or tokens[0] not in CONTRACT_MODULES:
            continue
        out.append(
            Replace(
                module=tokens[0],
                version=tokens[1] if len(tokens) > 1 else None,
                target=right.strip() or "(空)",
            )
        )
    return out


# ───────────────────────── 目标仓与 commit 解析 ─────────────────────────

def main_ref(repo: Path) -> str | None:
    """优先远程跟踪分支：本地 main 可能含未推送提交，不能代表「已合入」。"""
    for ref in ("refs/remotes/origin/main", "refs/heads/main"):
        if git(repo, "rev-parse", "--verify", "--quiet", ref).returncode == 0:
            return ref
    return None


def display_ref(ref: str) -> str:
    return ref.removeprefix("refs/remotes/").removeprefix("refs/heads/")


class TargetResolver:
    """按模块解析目标仓。同一模块只 clone 一次；失败也缓存，避免每个 pin 重试一次。"""

    def __init__(self, workspace: Path, offline: bool, cache_root: Path | None):
        self.workspace = workspace
        self.offline = offline
        self.cache_root = cache_root
        self.cache: dict[str, tuple[Target | None, str]] = {}

    def get(self, module: str) -> tuple[Target | None, str]:
        if module not in self.cache:
            self.cache[module] = self._resolve(module)
        return self.cache[module]

    def _resolve(self, module: str) -> tuple[Target | None, str]:
        name = CONTRACT_MODULES[module]
        local = self.workspace / name
        if (local / ".git").exists():
            ref = main_ref(local)
            if ref is None:
                return None, f"{local} 里找不到 main（refs/remotes/origin/main 或 refs/heads/main）"
            return Target(name, local, ref, "local"), ""

        if self.offline:
            return None, f"{name} 未在 {self.workspace} checkout，且 --offline 禁止 clone"
        if self.cache_root is None:
            return None, "内部错误：在线模式缺少临时 clone 目录"

        dest = self.cache_root / name
        if not dest.exists():
            url = f"https://{module}"
            r = git(
                None,
                "clone", "--bare", "--quiet", "--single-branch", "--branch", "main",
                url, str(dest),
            )
            if r.returncode != 0:
                return None, f"clone {url} 失败：{last_line(r.stderr)}"
        ref = main_ref(dest)
        if ref is None:
            return None, f"刚 clone 的 {name} 里找不到 main"
        return Target(name, dest, ref, "remote"), ""


def resolve_commit(repo: Path, version: str) -> tuple[str | None, str]:
    pseudo = PSEUDO_RE.match(version)
    if pseudo:
        short = pseudo.group(2)
        r = git(repo, "rev-parse", "--verify", "--quiet", f"{short}^{{commit}}")
        if r.returncode != 0:
            return None, (
                f"提交 {short} 不在 {repo.name} 的 main 历史中"
                f"（既不是 main 的祖先，也可能从未推送过）"
            )
        return r.stdout.strip(), ""

    if TAG_RE.match(version):
        tag = version.split("+", 1)[0]
        r = git(repo, "rev-parse", "--verify", "--quiet", f"refs/tags/{tag}^{{commit}}")
        if r.returncode != 0:
            return None, (
                f"{repo.name} 中找不到 tag {tag}"
                f"（未打 tag / 未推送 / 不在 main 历史中）"
            )
        return r.stdout.strip(), ""

    return None, f"无法解析的版本形态：{version}"


def check_pin(pin: Pin, resolver: TargetResolver) -> Verdict:
    target, err = resolver.get(pin.module)
    if target is None:
        return Verdict(False, f"{pin.consumer}: {pin.module} {pin.version} 无法核验 —— {err}")

    sha, err = resolve_commit(target.path, pin.version)
    if sha is None:
        return Verdict(False, f"{pin.consumer}: {pin.module} {pin.version} 无法核验 —— {err}")

    date_r = git(target.path, "show", "-s", "--format=%cI", sha)
    if date_r.returncode != 0:
        return Verdict(
            False,
            f"{pin.consumer}: {pin.module} {pin.version} 无法读取 {sha[:12]} 的提交日期"
            f" —— {last_line(date_r.stderr)}",
        )
    date = date_r.stdout.strip()
    ref = display_ref(target.ref)
    label = f"{target.name}@{sha[:12]} → {date}"

    anc = git(target.path, "merge-base", "--is-ancestor", sha, target.ref)
    if anc.returncode == 0:
        return Verdict(
            True,
            f"{label}（{pin.consumer} pin {pin.version}；{ref} 祖先；target={target.source}）",
        )
    if anc.returncode == 1:
        return Verdict(
            False,
            f"{label}（{pin.consumer} pin {pin.version}；**不是 {ref} 的祖先**"
            f"；若刚推过目标仓 main，先在目标仓 `git fetch origin main`）",
        )
    return Verdict(
        False,
        f"{label}（{pin.consumer} pin {pin.version}；ancestry 检查失败：{last_line(anc.stderr)}）",
    )


def check_consumers(
    consumers: list[Path],
    workspace: Path,
    offline: bool,
    cache_root: Path | None,
) -> dict[str, list[str] | int]:
    """扫描一组消费者仓，返回 oks / reds / notes / pin 数。"""
    resolver = TargetResolver(workspace, offline, cache_root)
    oks: list[str] = []
    reds: list[str] = []
    notes: list[str] = []
    pin_count = 0

    for consumer in consumers:
        gomod = consumer / "go.mod"
        if not gomod.exists():
            notes.append(f"{consumer.name}: 无 go.mod（跳过）")
            continue
        text = read(gomod)
        # 文件在但读不出来（空 / 非 UTF-8）不能当成「没有 pin」跳过 —— 那会把
        # 「没核成」伪装成「核过了」。go.mod 为空本身就是无效文件，判红。
        if not text.strip():
            reds.append(f"{consumer.name}: go.mod 为空或不可读（UTF-8 解码失败）—— 无法判定 pin")
            continue
        pins = find_pins(consumer.name, text)
        if not pins:
            notes.append(f"{consumer.name}: 无 contracts/kit pin")
            continue
        replaces = find_replaces(text)
        pin_count += len(pins)
        for pin in pins:
            overridden = [
                r for r in replaces
                if r.module == pin.module and (r.version is None or r.version == pin.version)
            ]
            if overridden:
                reds.append(
                    f"{pin.consumer}: {pin.module} 的 pin {pin.version} 被 replace 覆盖"
                    f" → {overridden[0].target}（实际编译的不是这个 pin）"
                )
                continue
            verdict = check_pin(pin, resolver)
            (oks if verdict.ok else reds).append(verdict.line)

    return {"oks": oks, "reds": reds, "notes": notes, "pins": pin_count}


# ───────────────────────── 消费者仓发现 ─────────────────────────

def select_consumers(workspace: Path, requested: list[str], core: Path) -> list[Path]:
    if requested:
        out: list[Path] = []
        for item in requested:
            p = Path(item)
            if not p.is_absolute():
                p = workspace / item
            if not p.exists():
                p = core / item
            if not (p / "go.mod").exists():
                raise SystemExit(f"--repo {item} 下没有 go.mod")
            out.append(p.resolve())
        return out

    out: list[Path] = []
    if (core / "go.mod").exists():
        out.append(core.resolve())
    if not workspace.is_dir():
        raise SystemExit(f"workspace 不存在：{workspace}")
    for child in sorted(workspace.iterdir()):
        if child.is_dir() and child.name.startswith("tellurion") and (child / "go.mod").exists():
            resolved = child.resolve()
            if resolved not in out:
                out.append(resolved)
    return out


# ───────────────────────── 自证腿 ─────────────────────────

def _git_do(path: Path, *args: str, env: dict[str, str] | None = None) -> str:
    r = git(path, *args, env=env)
    if r.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} 失败：{last_line(r.stderr)}")
    return r.stdout.strip()


def _commit(path: Path, filename: str, content: str, date: str, message: str) -> str:
    # 用 write_bytes 而不是 write_text(newline=...)：后者 3.10 才有 newline 形参，
    # 而本仓工具承诺 Python 3.9+；顺带避免 Windows 的 CRLF 翻译。
    (path / filename).write_bytes(content.encode("utf-8"))
    _git_do(path, "add", "-f", filename)
    env = {
        "GIT_AUTHOR_DATE": date,
        "GIT_COMMITTER_DATE": date,
        "GIT_AUTHOR_NAME": "fixture",
        "GIT_AUTHOR_EMAIL": "fixture@example.invalid",
        "GIT_COMMITTER_NAME": "fixture",
        "GIT_COMMITTER_EMAIL": "fixture@example.invalid",
    }
    # 夹具不依赖宿主机的签名 / hooks 配置：在任意开发机与 CI 上都该能提交。
    _git_do(path, "-c", "commit.gpgsign=false", "commit", "-q", "-m", message, env=env)
    return _git_do(path, "rev-parse", "HEAD")


def _pseudo(sha: str, stamp: str) -> str:
    return f"v0.0.0-{stamp}-{sha[:12]}"


def self_test() -> int:
    """护栏自证：真 git 夹具喂给检查函数，验它拦得住旁支 pin、也不误伤 main 上的提交。"""
    print("Quant Lab — check_contract_pin 护栏自证（夹具在临时目录，不触真仓）")
    if shutil.which("git") is None:
        print("  [SKIP] 本机无 git ⇒ 无法造 main/旁支/tag 夹具 —— 这是跳过，不是通过")
        print("::warning title=QT-09 pin 祖先自证未运行::本机无 git，11 条自证腿全部跳过。"
              "这不代表护栏自证通过。")
        return 0

    fails: list[str] = []

    def expect(label: str, ok: bool) -> None:
        print(f"  [{'ok' if ok else 'FAIL'}] {label}")
        if not ok:
            fails.append(label)

    date_a = "2026-10-01T12:00:00+08:00"
    date_b = "2026-10-02T12:00:00+08:00"
    date_c = "2026-10-03T12:00:00+08:00"
    date_k = "2026-10-04T12:00:00+08:00"

    with tempfile.TemporaryDirectory() as tmp:
        ws = Path(tmp)
        contracts = ws / "tellurion-contracts"
        contracts.mkdir()
        _git_do(contracts, "init", "-q", "-b", "main")
        sha_a = _commit(contracts, "a.txt", "a\n", date_a, "A")
        sha_b = _commit(contracts, "b.txt", "b\n", date_b, "B")
        _git_do(contracts, "checkout", "-q", "-b", "side", sha_a)
        sha_c = _commit(contracts, "side.txt", "c\n", date_c, "C（只在旁支）")
        _git_do(contracts, "-c", "tag.gpgSign=false", "tag", "v0.1.0", sha_b)
        _git_do(contracts, "-c", "tag.gpgSign=false", "tag", "v0.2.0", sha_c)

        kit = ws / "tellurion-kit"
        kit.mkdir()
        _git_do(kit, "init", "-q", "-b", "main")
        sha_k = _commit(kit, "k.txt", "k\n", date_k, "K")

        consumer = ws / "tellurion"
        consumer.mkdir()

        def run(gomod: str) -> dict[str, list[str] | int]:
            (consumer / "go.mod").write_bytes(gomod.encode("utf-8"))
            return check_consumers([consumer], ws, offline=True, cache_root=None)

        def block_mod(version: str, replace: str = "") -> str:
            return (
                "module tellurion\n\ngo 1.25.0\n\n"
                "require (\n"
                f"\tgithub.com/ruoxizhnya/tellurion-contracts {version} // indirect\n"
                ")\n" + replace
            )

        def single_mod(version: str) -> str:
            # 刻意用制表符分隔 require 与模块路径：验证解析器不依赖单个空格
            # （手写 / 编辑器改过的 go.mod 可能是 tab，写死空格会漏读）。
            return (
                "module tellurion\n\ngo 1.25.0\n\n"
                f"require\tgithub.com/ruoxizhnya/tellurion-contracts {version}\n"
            )

        def kit_mod(version: str) -> str:
            return (
                "module tellurion\n\ngo 1.25.0\n\n"
                f"require github.com/ruoxizhnya/tellurion-kit {version}\n"
            )

        res = run(block_mod(_pseudo(sha_a, "20261001120000")))
        expect("pin 指向 main 中段提交（祖先但不等于 tip）→ 绿",
               not res["reds"] and len(res["oks"]) == 1 and res["pins"] == 1)
        expect("输出含 `repo@sha → 提交日期`",
               any(f"tellurion-contracts@{sha_a[:12]} → {date_a}" in x for x in res["oks"]))

        res = run(block_mod(_pseudo(sha_b, "20261002120000")))
        expect("pin 指向 main tip → 绿", not res["reds"] and len(res["oks"]) == 1)

        res = run(kit_mod(_pseudo(sha_k, "20261004120000")))
        expect("kit pin 指向 main → 绿且打印 kit@sha → 日期",
               not res["reds"] and len(res["oks"]) == 1
               and any(f"tellurion-kit@{sha_k[:12]} → {date_k}" in x for x in res["oks"]))

        res = run(block_mod(_pseudo(sha_c, "20261003120000")))
        # 夹具没有 origin 远程 ⇒ 判据落回 refs/heads/main；断言只钉「不是 main 的祖先」，
        # 不把 ref 名写死（真实仓通常走 origin/main）。
        expect("pin 指向旁支提交 → 红（不是 main 祖先）",
               bool(res["reds"]) and all("不是 main 的祖先" in x for x in res["reds"]))

        res = run(single_mod("v0.1.0"))
        expect("tag 打在 main 上 → 绿", not res["reds"] and len(res["oks"]) == 1)

        res = run(single_mod("v0.2.0"))
        expect("tag 打在旁支上 → 红（不是 main 祖先）",
               bool(res["reds"]) and all("不是 main 的祖先" in x for x in res["reds"]))

        res = run(block_mod(_pseudo("deadbeefdead", "20261004120000")))
        expect("幽灵 sha → 红（无法解析，不静默放行）",
               bool(res["reds"]) and all("不在 tellurion-contracts 的 main 历史中" in x
                                        for x in res["reds"]))

        res = run("")
        expect("空 go.mod → 红（不可读不静默跳过）",
               bool(res["reds"]) and all("go.mod 为空或不可读" in x for x in res["reds"]))

        res = run("module tellurion\n\ngo 1.25.0\n")
        expect("无 pin → 绿且显式记 0 pin",
               not res["reds"] and res["pins"] == 0
               and any("无 contracts/kit pin" in x for x in res["notes"]))

        res = run(block_mod(
            _pseudo(sha_b, "20261002120000"),
            "\nreplace github.com/ruoxizhnya/tellurion-contracts => ../tellurion-contracts\n",
        ))
        expect("replace 覆盖 pin → 红（require 里的版本不是实际编译版本）",
               bool(res["reds"]) and all("replace 覆盖" in x for x in res["reds"]))

    if fails:
        print(f"\n结果：护栏自证 RED —— {len(fails)} 条：{fails}")
        return 1
    print("\n结果：护栏自证 GREEN —— 11 条腿：拦得住旁支 pin/tag、幽灵 sha、replace、空 go.mod，"
          "也放行 contracts/kit 在 main 上的中段提交与 tag。")
    return 0


# ───────────────────────── main ─────────────────────────

def _utf8_streams() -> None:
    """Windows 控制台默认 GBK；本工具输出中文，不强制 UTF-8 会在本机打成乱码。"""
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8")
        except (AttributeError, OSError, ValueError):
            pass


def main(argv: list[str] | None = None) -> int:
    _utf8_streams()
    ap = argparse.ArgumentParser(
        description="QT-09 ①：契约 pin 必须是 contracts/kit 仓 main 的祖先"
    )
    ap.add_argument("--workspace", type=Path, default=None,
                    help="兄弟仓所在目录（默认 core 的父目录）")
    ap.add_argument("--repo", action="append", default=[],
                    help="只扫指定仓（名字或路径；可重复）")
    ap.add_argument("--offline", action="store_true",
                    help="只用本地 checkout，禁止匿名 clone（离线/自证用）")
    ap.add_argument("--self-test", action="store_true",
                    help="护栏自证：真 git 夹具，验拦得住旁支、也不误伤合规 pin")
    args = ap.parse_args(argv)

    if args.self_test:
        return self_test()

    core = core_dir()
    workspace = (args.workspace or core.parent).resolve()
    consumers = select_consumers(workspace, args.repo, core)

    cache_root: Path | None = None
    if not args.offline:
        cache_root = Path(tempfile.mkdtemp(prefix="contract-pin-"))
    try:
        result = check_consumers(consumers, workspace, args.offline, cache_root)
    finally:
        if cache_root is not None:
            shutil.rmtree(cache_root, ignore_errors=True)

    print("Quant Lab — contract pin ancestry check (QT-09 ①)\n")
    print(f"workspace = {workspace}")
    print(f"扫描 {len(consumers)} 个 go 仓；pin 数 = {result['pins']}\n")
    for line in result["notes"]:
        print(f"  [skip] {line}")
    for line in result["oks"]:
        print(f"  [ok] {line}")
    for line in result["reds"]:
        print(f"  [red] {line}")

    if result["reds"]:
        print(f"\n结果：RED —— {len(result['reds'])} 个 pin 未通过祖先校验"
              f"（共扫描 {result['pins']} 个 pin）。")
        return 1
    if result["pins"] == 0:
        print(f"\n结果：GREEN —— 未发现 contracts/kit pin（0 个），无需祖先校验；"
              f"已扫 {len(consumers)} 个仓。")
        return 0
    print(f"\n结果：GREEN —— {result['pins']} 个 pin 全部是目标仓 main 的祖先；"
          f"{len(result['notes'])} 个仓无 pin / 跳过。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
