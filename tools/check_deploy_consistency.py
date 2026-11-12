#!/usr/bin/env python3
"""校验 docker-compose.yml 与 deploy/k8s/*.yaml 的关键项是否一致（P1-8）。

背景：本地开发走 docker-compose，部署走 k8s，两套配置独立维护 —— 改了一边
忘了另一边就会漂移，而且漂移往往是**静默**的：服务起得来，只在某个调用
跨服务时才失败（比如 analysis 找不到 data-service）。

**2026-11-12 起 k8s 形态已退役（CORE-K8S-EXIT / D7=a）**：`deploy/k8s/` 只留
`README.md`（退役说明）与 `archive/`（历史清单），唯一编排是仓根
`docker-compose.yml`。因此 1 / 2 的 k8s 半边、5~8 的 k8s 断言在退役状态下
整段跳过（main 会打 note 明说跳过的是**已退役的对象**），compose 侧断言一条
不减；活跃层一旦出现清单即报错 —— 「重新采用 k8s」要立 ADR，不能靠放回文件。

本脚本只比对**会互相影响**的关键项：
  1. 服务名 → 端口（公共服务必须一致）
  2. DATA_SERVICE_URL：compose 的 environment 与 k8s configmap 必须一致，
     且它指向的 host:port 要真的等于 data-service 的端口
  3. 数据库必须来自**宿主机原生安装**，且容器**显式**指向它（AUD-13 改写）。
     2026-09-25 之前这条是「compose 的 postgres / redis 服务端口只绑回环」——
     那两个服务已移出 compose（改由宿主机原生安装），端口映射这个**检查对象
     本身不存在了**。新的等价断言有两半：
       a) compose 里不得再出现 postgres / redis 服务（留着会与原生进程抢端口）；
       b) 应用侧的 DATABASE_HOST 必须指向 host.docker.internal。
     后半条接替了被移除的 `depends_on + service_healthy`：容器里的 localhost
     指容器自己，写成 localhost 语法完全合法、启动才失败。
     2026-11-10：缓存侧（REDIS_URL）随 data 面退役一起失去**全部读取点**，
     已从 configmap/compose 删除，本条不再断言之 —— 见 INFRA_ENV_REQUIRED 注。
     原生进程的 listen_addresses / bind 由 tools/local-infra.sh 在机器上断言 ——
     绑定的真身在仓外，静态脚本管不到它，写在这里只会变成假护栏。
  4. 部署配置里不得出现 GIN_MODE（AUD-29）。gin mode 的唯一来源是
     config/*.yaml 的 server.gin_mode；GIN_MODE 会被 gin 自己读走，是同一个
     决定的第二个入口，而且 grep 不到读取点。
  3c. open-access 的声明与端口发布范围必须**互相蕴含**（AUD-52 / P0-4）。
     容器必须绑 0.0.0.0 才能被发布端口转发，于是「open-access 只许在 loopback
     上」这条不变量判不了绑定地址，只能判**发布层**。cmd/analysis 因此承认一个
     显式声明 AUTH_INSECURE_EXPOSURE=loopback-published —— 而声明本身不做任何
     保证，保证是这一项给的。双向四条：开了 open-access 就不许有非回环映射；
     有非回环映射就必须有 JWT_SECRET；开了 open-access 必须有那个声明；
     同时给密钥与声明 → 报错（密钥优先，声明会误导读者）。
  5. compose ↔ k8s 的数据库 env **口径**必须一致，且库名/用户名
     不能与 config/*.yaml 漂移（AUD-39）。
     2026-09-25 **放宽**：地址（DATABASE_HOST 的 host）不再比对 ——
     数据库移到宿主机后容器内必然是 host.docker.internal，而 k8s 里是 Service
     名 postgres，两者**必然不同**，比它只会逼出一个恒假的断言。仍然比对：
     库名、用户名、端口，以及密码取自同一个变量。config/*.yaml 的 host 现在是
     宿主机视角（localhost），同样不参与比对。
     2026-11-10：data-service 的 env 口径不再是这五个键，而是一个 DATABASE_URL
     （+ DATA_MARKET_SCHEMA），因此它移出 5a/5b/5c 的逐服务循环，改由 5e 守：
     compose 的 DSN 必须引用 ${DATABASE_PASSWORD}、k8s 的 DSN 必须由五个
     `$(...)` 字段拼出、两边的 DATA_MARKET_SCHEMA 必须同值且非空。
     2026-11-11：ai-service 的 YAML 随 AI 拆仓归 tellurion-agent（本仓那份已删），
     5c 对它改走两条 —— 本仓 config/ai-service.yaml 若重新出现即报错；同工作区
     存在 agent 仓时对拍它的 yaml 与 compose 里 ai-service 自己的 env，找不到
     agent 仓时打 note 明说跳过（不静默）。
  6. 注入到应用容器的每个 env 名都必须有读取点（AUD-39）。读取点从 Go 源码
     的 Get*/UnmarshalKey/BindEnv/Sub 调用点推导，不是维护一张手写清单。
  7. config/*.yaml 里不得出现 ${...}（AUD-39）。本仓没有 env 展开器，占位符
     不是模板，是字面量 —— 它曾被当成真的密码 / token 用。
  7b. compose 的比对类断言**先把 `${VAR:-default}` 展开再比**（2026-11-12）。
     不展开时，`DATABASE_DATABASE: ${DATABASE_NAME:-tellurion}` 与 k8s 的
     `tellurion` 是「字符串不等、语义相等」，5b/5c 会恒红（实测 4 条假阳性）；
     反过来若把 default 改坏，不展开也**看不出来**。展开只用于**比对**，配置
     原文一字不改（`${DATABASE_PASSWORD}` 这类「必须引用某变量」的断言仍读原文）。
     另加 5f：compose 里每一处 `${DATABASE_NAME:-...}` 的默认值必须相同且与
     k8s / config 的库名一致 —— 5b/5c 只覆盖 analysis / ai 两个服务，
     实测 10 处引用里有 6 处（data-service 的 DSN、worker / dualrun 的
     DATABASE_DATABASE、full profile 四个跨仓服务的 DSN）没人比。
  8. 死键负向钉（AUD-39）：POSTGRES_HOST/PORT、REDIS_HOST/PORT、
     REDIS_PASSWORD、DB_PASSWORD 不许回来；POSTGRES_DB/USER/PASSWORD 只许
     出现在 postgres 容器自己的 env 里。
  9. Dockerfile 的 COPY 源路径必须存在（AUD-48）。
  10. compose 的 build 面必须指向本仓真实存在的 Dockerfile，且 Dockerfile
      里的 `go build ./cmd/xxx` 目标目录必须还在（CORE-CI-IMAGE-GATE /
      ODR-072）。这一项是 CI `image-build` job 的静态半边：CI 真跑
      `docker build -f Dockerfile.service .`，本地四条门禁靠本项兜底 ——
      否则「compose 声明了镜像、镜像从来没构建过」在本地完全不可见。

刻意不做的事：
  - 不做「一份源生成两份」。那要引入 Kompose / Helm 之类的工具，为了
    几处配置给单人自托管项目加一整套生成链，收益不抵复杂度。
  - 不要求服务集合完全相同。k8s 里没部署 ai-service 是**有意的**（AI 拆仓
    阶段 1），写死在 ALLOWED_K8S_ONLY_MISSING 里。

检查 6 的**边界**（写出来，免得被当成全覆盖）：
  - 「env 可达性」的基准是 Go 源码里 `Get*` / `UnmarshalKey` / `Sub` /
    `BindEnv` / `os.Getenv` 的调用点，加上 `ConfigKey* = "a.b"` 这类常量定义。
  - **不覆盖** `viper.Unmarshal(&整个结构体)` 读的键（只能靠结构体 tag 反推，
    代价不划算）。这类键若进了 configmap 会被**误报**，加进
    ENV_NAME_ALLOWLIST 并写明理由 —— 宁可误报，不可漏报。
  - 只查 k8s 的 analysis/data 两个 deployment（应用容器）。postgres / redis
    的 env 是**容器镜像自己的**约定（POSTGRES_*），不由本仓 Go 代码消费，
    因此不参与可达性检查，只参与「不许作为 configmap 键」的负向检查。
  - 数据服务自 SVC-CORE-DATA-RETIRE（2026-11-08）起由 tellurion-data 仓发布，
    它的读取点**不在本仓源码里**。这类跨仓键在 CROSS_REPO_ENV_READERS 里逐条
    声明读取方（TUSHARE_TOKEN / DATABASE_URL / DATA_MARKET_SCHEMA）；未声明的
    仍按零读取点报错。

只用标准库（这个环境没有 pyyaml），靠正则抓规整的 YAML 缩进块。
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
COMPOSE = ROOT / "docker-compose.yml"
K8S_DIR = ROOT / "deploy" / "k8s"

# k8s 里有意不部署的服务（及其原因）。改这个清单本身就该被 review。
#
# ⚠️ postgres / redis 两条已于 2026-09-25 删除：它们不再是 compose 服务
# （改由宿主机原生安装），所以检查 1 根本遍历不到它们 —— 留着是**死条目**，
# 看起来在解释什么、实际永远查不到。「它们不许回到 compose」这件事现在由
# 检查 3a 正向断言。
ALLOWED_MISSING_IN_K8S = {
    # AI 拆仓阶段 1：k8s 侧暂不部署 ai-service（本地 compose 先行验证；
    # k8s 清单随阶段 2 搬仓一并补 —— 到时本条移除）。
    "ai-service": "AI 拆仓阶段 1，k8s 清单待阶段 2 补",
    # 2026-11-11 单一 compose 合并：full profile / 默认栈的执行网关是跨仓服务，
    # core 的 k8s 清单不托管它们（各自成仓部署）。它们进 compose 是为了本地
    # 一条命令起全栈，不代表 k8s 要补这五个 Service。
    "broker": "跨仓 tellurion-broker；k8s 清单不托管执行网关",
    "risk": "跨仓 tellurion-risk；k8s 清单不托管风控服务",
    "strategy": "跨仓 tellurion-strategy；k8s 清单不托管策略服务",
    "analytics": "跨仓 tellurion-analytics；k8s 清单不托管分析服务",
    "orchestration": "跨仓 tellurion-orchestration；k8s 清单不托管编排服务",
}

# 两边都可能不配的最小集合（比如只在本地跑的辅助容器）
IGNORED_SERVICES = {"equitydeep-research"}

# AUD-13（2026-09-25 改写）：数据库/缓存不再由 compose 托管，改由宿主机原生
# 安装提供（PostgreSQL 16.15 运行实例 / Redis 7.4.11，见 docker-compose.yml 文件头）。
#
# 原来这里有一对常量（LOOPBACK_ONLY / LOOPBACK_BINDS）守的是 compose 的 ports
# 映射 —— 那两个服务已从 compose 移除，**检查对象不存在了**。留着不改的后果不是
# 报错，而是检查 3 空转：循环体一次都不进，脚本照常打「✓ 数据库/缓存只绑回环」。
# 那是典型的假护栏（比没护栏更糟）。
#
# 换成下面这组：守「基础设施确实在仓外」+「容器确实指向它」。
NATIVE_DB_DEPS = ("postgres", "redis")

# 容器内用来指宿主机的名字。**不能是 localhost** —— 容器里的 localhost 指容器
# 自己。实测（2026-09-25）：Docker Desktop 默认把它解析成 IPv4 192.168.65.254，
# 且能连到宿主机回环上监听的服务。
CONTAINER_HOST_ALIAS = "host.docker.internal"

# 每个应用服务**必须显式**注入的基础设施 env：(env 名, 断言方式)。
# 这是本轮改造后的承重项 —— config/*.yaml 现在的口径是宿主机视角（localhost），
# 容器服务一旦漏注入就会静默回落到 localhost 并连到容器自己；compose 语法合法、
# 启动日志也只是一句连接失败。此前这层保证由 depends_on 提供，现在没有了。
INFRA_ENV_REQUIRED: dict[str, tuple[tuple[str, str], ...]] = {
    # 2026-11-10：REDIS_URL 条目移除 —— data 面退役（SVC-CORE-DATA-RETIRE）后
    # 本仓没有任何运行时读取点再读 redis.url，configmap 与 compose 里的该键
    # 同批删除。等缓存接线回来时，这里与 APP_DB_ENV_KEYS 一起加回（只加 env
    # 不加这两处，检查 6 会把它当死键报出来）。
    # 2026-11-10：data-service 的条目从 DATABASE_HOST 改成 DATABASE_URL —— 它的
    # 镜像自 SVC-CORE-DATA-RETIRE（2026-11-08）起由 tellurion-data 仓发布，只读
    # 一个 DSN，不再读 core 的 DATABASE_HOST/PORT/USER/DATABASE 四键。缺 DSN 时
    # 它起得来但**连不到库**，而 compose 语法完全合法 —— 用 "url" 断言地址段必须
    # 指向宿主机（不能是 localhost）。
    "data-service": (("DATABASE_URL", "url"),),
    "analysis-service": (("DATABASE_HOST", "host"),),
    # REV-16（2026-10-17）：strategy-service 已删除，条目随之移除 ——
    # 留在表里会成为一个永远查不到的死条目（与文件头 postgres/redis 的教训同型）。
}


# 端口映射的三种写法都要认：
#   "5432"                  → 只暴露容器端口，宿主端口随机（本仓未用）
#   "5432:5432"             → 绑所有网卡
#   "127.0.0.1:5432:5432"   → 只绑回环
#
# ⚠️ AUD-13 之前这个正则只认两段式。加上回环前缀后若不改它，postgres / redis
# 会**静默**从 compose_svc 里消失 —— 而两者都在 ALLOWED_MISSING_IN_K8S 里，
# 只出 note 不报错，护栏就此失效（「两个机制各自对、接起来就错」）。
# 以后再改端口写法，务必同时验这条正则还认不认。
_PORT_LINE = re.compile(r'^\s*-\s*"?((?:[\w.\-]+):)?(\d+):(\d+)"?\s*$')


def parse_compose_ports(path: Path) -> dict[str, list[tuple[str | None, int]]]:
    """抓 docker-compose 的 service → [(宿主绑定地址 or None, 宿主端口)]。

    绑定地址为 None 表示端口映射没写宿主地址，等价于绑 0.0.0.0。
    """
    bindings: dict[str, list[tuple[str | None, int]]] = {}
    current: str | None = None
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        m = re.match(r"^ {2}([A-Za-z0-9_.-]+):\s*$", line)
        if m:
            current = m.group(1)
            continue
        m = _PORT_LINE.match(line)
        if m and current:
            bind = m.group(1).rstrip(":") if m.group(1) else None
            bindings.setdefault(current, []).append((bind, int(m.group(2))))
    return bindings


def active_k8s_manifests() -> list[Path]:
    """活跃层清单 = `deploy/k8s/` 下**递归**的 `*.yaml`，排除 `archive/`。

    递归而非仅顶层是刻意的：若将来有人把清单塞进 `deploy/k8s/prod/*.yaml`，
    只扫顶层会把「k8s 回来了」误判成「活跃层 0 份、已退役」。同样地，改名成
    `*.yml` 也会被 `rglob("*.yaml")` + 下面的 `.yml` 补扫兜住。
    """
    out: list[Path] = []
    for pattern in ("*.yaml", "*.yml"):
        for p in sorted(K8S_DIR.rglob(pattern)):
            rel = p.relative_to(K8S_DIR)
            if rel.parts and rel.parts[0] == "archive":
                continue
            out.append(p)
    return out


def k8s_manifest(name: str) -> Path:
    """按文件名在活跃层里找清单（允许放在子目录）；找不到时回落到顶层路径，
    由调用方的存在性断言报错，而不是在解析处抛 FileNotFoundError。"""
    for p in active_k8s_manifests():
        if p.name == name:
            return p
    return K8S_DIR / name


def parse_k8s_ports() -> dict[str, int]:
    """抓 k8s Service 的 name → port（Service 才是别的 Pod 访问用的入口）。"""
    ports: dict[str, int] = {}
    for f in active_k8s_manifests():
        text = f.read_text(encoding="utf-8")
        for doc in text.split("\n---\n"):
            if not re.search(r"^kind:\s*Service\s*$", doc, re.M):
                continue
            name = re.search(r"^ {2}name:\s*(\S+)\s*$", doc, re.M)
            port = re.search(r"^\s*port:\s*(\d+)\s*$", doc, re.M)
            if name and port:
                ports[name.group(1)] = int(port.group(1))
    return ports


# ── CORE-K8S-EXIT：k8s 形态已退役（D7=a）───────────────────────────────
#
# 2026-11-12 起唯一编排是仓根 `docker-compose.yml`（CORE-COMPOSE-UNIFY）；k8s 清单
# 整体移入 `deploy/k8s/archive/`，只作复盘物证。本函数只认两种状态：
#   - 退役完成：archive/ 存在 **且** 活跃层 0 份清单 → 返回 True，所有
#     compose↔k8s 对比整段跳过（并打一条 note 明说跳过的是「已退役的对象」，
#     不是静默失效）；
#   - 未退役：有活跃清单 → 返回 False，原有对比照跑。
#
# 活跃层一旦出现清单就**报错**而不是恢复对比：那等于两套编排悄悄回来，正确做法
# 是另立 ADR（`deploy/k8s/README.md` 的「重新采用 k8s 的流程」）。同理，
# 「没有清单也没有 archive/」是半退役状态，也不许默认放行。
def k8s_is_retired(errors: list[str], notes: list[str]) -> bool:
    # 递归扫（排除 archive/），不是只看顶层 —— 子目录里放清单同样算「k8s 回来了」，
    # 只改文件名/加一层目录都不该让退役判定漏判。
    active = [p.relative_to(K8S_DIR).as_posix() for p in active_k8s_manifests()]
    archived = K8S_DIR / "archive"
    if archived.is_dir():
        if active:
            errors.append(
                f"deploy/k8s 活跃层仍有 {len(active)} 份清单（{', '.join(active)}）"
                f"—— k8s 已于 CORE-K8S-EXIT（2026-11-12 / D7=a）退役，唯一编排是 "
                f"docker-compose.yml；恢复 k8s 须另立 ADR，不能把清单直接放回来")
        else:
            notes.append(
                "deploy/k8s 清单已退役（CORE-K8S-EXIT / D7=a，2026-11-12）：活跃层 "
                "0 份，compose↔k8s 的端口/URL/Schema/身份对比整段跳过；"
                "历史清单只读在 deploy/k8s/archive/")
        return True
    if not active:
        errors.append(
            "deploy/k8s 既没有活跃清单也没有 archive/ 退役标记 —— 半退役状态无法判定"
            "（要么按 CORE-K8S-EXIT 退役，要么把清单放回来）")
        return True
    return False


def find_env_value(text: str, key: str) -> str | None:
    m = re.search(rf'^\s*{re.escape(key)}:\s*"?([^"\n]+?)"?\s*$', text, re.M)
    return m.group(1).strip() if m else None


def find_nested_value(text: str, section: str, key: str) -> str | None:
    """抓 `section:` 块里第一层的 `key: value`（2 空格缩进）。"""
    in_section = False
    for line in text.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if re.match(rf"^{re.escape(section)}:\s*$", line):
            in_section = True
            continue
        if not in_section:
            continue
        if re.match(r"^\S", line):  # 到了下一个顶层键，section 结束
            return None
        m = re.match(r'^ {2}([a-z_]+):\s*"?([^"\n]*?)"?\s*$', line)
        if m and m.group(1) == key:
            return m.group(2).strip()
    return None


# ── AUD-39：env 口径护栏 ──────────────────────────────────────────────
#
# 这一组针对的是一类**静默**失败：部署配置里注入的 env 名与代码实际读的配置
# 键推导出的 env 名不是同一个字符串。服务照常启动，只在真正用到时才失败，而且
# 失败信息（"password authentication failed"）读起来像「值错了」而不是
# 「名字错了」—— k8s 侧曾同时给错键名（POSTGRES_* vs DATABASE_*）和错值
# （quantlab vs tellurion）。
#
# 基准是 viper 的推导规则：AutomaticEnv + SetEnvKeyReplacer(".", "_") 把配置键
# `database.host` 映射成 env 名 **DATABASE_HOST**。这条规则一旦改，下面两个检查
# 都要跟着改。

_GO_KEY_CALL = re.compile(
    r"(?:GetString|GetInt|GetInt64|GetBool|GetFloat64|GetDuration|GetStringSlice"
    r"|GetStringMap|GetStringMapString|GetTime|BindEnv)"
    r'\(\s*"([a-z0-9_.]+)"'
)
# `BindEnv("auth.jwt_secret", "JWT_SECRET", "AUTH_JWT_SECRET")` 的第 2 个及
# 以后的实参才是**真正的 env 名**；只按第一个实参推导会漏掉别名（AUD-39 曾因此
# 把 analysis / data 清单里的 `JWT_SECRET` 误报成「零读取者」）。
_GO_BINDENV_CALL = re.compile(r'\.BindEnv\(([^)\n]*)\)')
_GO_ENV_ALIAS_LITERAL = re.compile(r'"([A-Z][A-Z0-9_]*)"')
# viper.UnmarshalKey("trading", …) 覆盖整个子树 → 记成前缀 TRADING_。
_GO_UNMARSHAL_KEY_CALL = re.compile(r'UnmarshalKey\(\s*"([a-z0-9_.]+)"')
# viper.Sub("backtest") 同理。注意 time.Time.Sub 也命中 `.Sub(`，但它的实参
# 不是字符串字面量，不会匹配。
_GO_SUB_CALL = re.compile(r'\.Sub\(\s*"([a-z0-9_.]+)"')
_GO_GETENV_CALL = re.compile(r'os\.Getenv\(\s*"([A-Z0-9_]+)"')
# 有些键是通过常量读的（`v.GetString(httpserver.ConfigKeyGinMode)`），字面量
# 正则抓不到 —— 把常量定义一起收进来，而不是维护一张手写清单。
_GO_CONFIG_KEY_CONST = re.compile(r'ConfigKey[A-Za-z0-9_]*\s*=\s*"([a-z0-9_.]+)"')

# 应用读的数据库 env 名（5a 用它断言「必备键都在」）。
#
# 2026-11-10：REDIS_URL 从这里移除（data 面退役后本仓零读取点）。
#
# 注意 5b **不再**拿这个清单去逐一比值：DATABASE_HOST 在三处口径
# 不同且都正确 —— config/*.yaml 是宿主机视角（localhost）、compose 是
# host.docker.internal、k8s 是 Service 名 postgres。比值的清单见 5b 里那几个
# 身份字段（库名 / 用户名 / 端口）。
APP_DB_ENV_KEYS = [
    "DATABASE_HOST",
    "DATABASE_PORT",
    "DATABASE_USER",
    "DATABASE_DATABASE",
]

# 5b 真正拿来比值的「身份」字段：连过去之后必须对得上的东西。
# 刻意**不含** DATABASE_HOST 的 host —— 它按环境各写一份是对的。
IDENTITY_ENV_KEYS = ["DATABASE_USER", "DATABASE_DATABASE", "DATABASE_PORT"]
# 2026-11-10：data-service 移出本清单 —— 它的 env 口径已不是 core 的五个
# DATABASE_* 键，而是一个 DATABASE_URL（+ DATA_MARKET_SCHEMA）。5a/5b/5c 拿
# 这组键去要求它会逼它把已删的键加回来；它的口径由 5e 单独守，地址段由 3b 守。
APP_ENV_SERVICES = ["analysis-service", "ai-service"]

# 5c 逐服务对拍 config/*.yaml 的清单：**本仓自带 YAML** 的那些。
# 2026-11-11：ai-service 移出 —— 它的 YAML 随 AI 拆仓归 tellurion-agent，本仓
# 那份已删。5c-2 单独守「本仓那份不许回来」+「与 agent 仓那份对拍」。
# 注意 5a/5b 仍用 APP_ENV_SERVICES：它们只比 compose ↔ k8s，不读本仓 yaml。
CONFIG_YAML_SERVICES = ["analysis-service"]

# 在部署配置里**不许出现**的 env 名 —— 每一条都是「曾经存在过的死键」。
BANNED_ENV_NAMES = {
    "POSTGRES_HOST": "零读取者：应用读 database.host → DATABASE_HOST，"
                     "postgres 容器也不需要知道自己的 host",
    "POSTGRES_PORT": "零读取者：同上",
    "REDIS_HOST": "零读取者：应用读 redis.url → REDIS_URL",
    "REDIS_PORT": "零读取者：同上",
    "REDIS_PASSWORD": "零读取者：本项目 redis 没有 --requirepass，不校验密码",
    "DB_PASSWORD": "零读取者：viper 推导出的名字是 DATABASE_PASSWORD。"
                   "曾与 DATABASE_PASSWORD 并存，默认值相同所以能用，改密码就必然对不上",
}

# postgres 容器**自己**的约定名：只允许出现在 postgres 自己的 env 里
# （compose 的 postgres 服务 / k8s 的 postgres-statefulset）。应用侧一律用
# DATABASE_*。
POSTGRES_CONTAINER_ENV = {"POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD"}


def derive_readable_env_names() -> tuple[set[str], set[str]]:
    """从 Go 源码推导「应用能读到的 env 名」。

    只看**非测试**文件：测试读某个键不代表生产读它。返回 (精确名, 前缀)。

    覆盖的读取形态：`Get*("a.b")` / `BindEnv("a.b")` / `os.Getenv("A")` →
    精确名；`BindEnv("a.b", "A", "B")` 的别名实参 A / B 也是精确名；
    `UnmarshalKey("a")` / `Sub("a")` → 前缀 `A_`（整棵子树）；
    `ConfigKey* = "a.b"` 这类常量定义 → 精确名。

    **不覆盖** `viper.Unmarshal(&整个结构体)`（已删的 cmd/strategy 曾这样读 server.*）——
    它读的键只能靠结构体 tag 反推，代价不划算。这类键若进了 configmap 会被
    误报，加进 ENV_NAME_ALLOWLIST 并写明理由即可（宁可误报，不可漏报）。
    """
    names: set[str] = set()
    prefixes: set[str] = set()
    for path in sorted(ROOT.rglob("*.go")):
        if path.name.endswith("_test.go") or ".workbuddy-ai" in path.parts:
            continue
        text = path.read_text(encoding="utf-8", errors="replace")
        for m in _GO_KEY_CALL.finditer(text):
            names.add(m.group(1).upper().replace(".", "_"))
        for m in _GO_BINDENV_CALL.finditer(text):
            # 第一实参是配置键（上面已覆盖），这里补别名实参。
            for alias in _GO_ENV_ALIAS_LITERAL.finditer(m.group(1)):
                names.add(alias.group(1))
        for m in _GO_CONFIG_KEY_CONST.finditer(text):
            names.add(m.group(1).upper().replace(".", "_"))
        for regex in (_GO_UNMARSHAL_KEY_CALL, _GO_SUB_CALL):
            for m in regex.finditer(text):
                prefixes.add(m.group(1).upper().replace(".", "_") + "_")
        for m in _GO_GETENV_CALL.finditer(text):
            names.add(m.group(1))
    return names, prefixes


# 不来自上面的推导、但确实会被读到的 env 名。**加条目必须写理由** ——
# 空集合本身就是一条信息：现在没有任何例外。
ENV_NAME_ALLOWLIST: dict[str, str] = {}

# 由**本仓之外的镜像**读取的 env 名：值 = 读取方（哪个仓、哪段代码）。
#
# 为什么需要这一层：data-service 的镜像自 SVC-CORE-DATA-RETIRE（2026-11-08）
# 起由 tellurion-data 仓发布，而本检查的推导基准是**本仓** Go 源码。跨仓读取
# 点因此天然失明 —— 若不声明，只剩「删掉这个 env」一条路，那会让跨仓读方
# 静默失去配置（比误报危险得多）。
#
# 约束：条目必须写清读取方；本仓一旦出现同名读取点，它就会自然变成可推导的，
# 届时应把条目删掉（留着的死声明和死键同罪）。
CROSS_REPO_ENV_READERS: dict[str, str] = {
    "TUSHARE_TOKEN": "tellurion-data：数据服务的 Tushare 传输（X1c）读取本 env；本仓只负责注入",
    "DATABASE_URL": "tellurion-data：cmd/dataservice/main.go 读这一个 DSN 建连接池（不再读 DATABASE_HOST/PORT/USER/DATABASE）",
    "DATA_MARKET_SCHEMA": "tellurion-data：cmd/dataservice/main.go 传给 store.Options.MarketSchema（X1d 方向，出厂 market）",
    "STRATEGY_ASSET_URL": "tellurion-agent：cmd/ai/lifecycle.go BindEnv strategy_asset.url，cmd/ai/endpoints.go 解析主机名（QT-32-B）",
    "STRATEGY_ASSET_TOKEN": "tellurion-agent：cmd/ai/lifecycle.go BindEnv strategy_asset.token（空 = 不发 Authorization，启动期 warn；QT-32-B）",
}


def is_readable(name: str, names: set[str], prefixes: set[str]) -> bool:
    if name in names or name in ENV_NAME_ALLOWLIST or name in CROSS_REPO_ENV_READERS:
        return True
    return any(name.startswith(p) for p in prefixes)


def parse_compose_environment(path: Path) -> dict[str, dict[str, str]]:
    """抓 docker-compose 每个 service 的 environment: 键值（原样，不展开插值）。"""
    envs: dict[str, dict[str, str]] = {}
    current: str | None = None
    in_env = False
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        m = re.match(r"^ {2}([A-Za-z0-9_.-]+):\s*$", line)
        if m:
            current, in_env = m.group(1), False
            continue
        if re.match(r"^ {4}environment:\s*$", line):
            in_env = True
            continue
        if not in_env:
            continue
        m = re.match(r"^ {6}([A-Z][A-Z0-9_]*):\s*(.*)$", line)
        if m:
            envs.setdefault(current, {})[m.group(1)] = m.group(2).strip().strip('"')
        elif not line.startswith("      "):
            in_env = False
    return envs


def parse_configmap_keys(text: str) -> dict[str, str]:
    """抓 configmap `data:` 下 2 空格缩进的 UPPER_CASE 键。"""
    return {
        m.group(1): m.group(2).strip().strip('"')
        for m in re.finditer(r"^ {2}([A-Z][A-Z0-9_]*):\s*(.*)$", text, re.M)
    }


def parse_deployment_env_names(path: Path) -> set[str]:
    """抓 deployment 里 `- name: UPPER_CASE` 的 env 名。

    containerPort / container 的 `- name: http` 是小写，不会误命中。
    """
    return set(
        re.findall(r"^\s*- name: ([A-Z][A-Z0-9_]*)\s*$",
                   path.read_text(encoding="utf-8"), re.M)
    )


def parse_deployment_env_values(path: Path) -> dict[str, str]:
    """抓 deployment 里 `- name: KEY` 紧跟着的**字面量** `value: "..."`。

    走 valueFrom / secretKeyRef 的条目不进返回值 —— 它们没有静态值可查。
    """
    values: dict[str, str] = {}
    current: str | None = None
    for line in path.read_text(encoding="utf-8").splitlines():
        m = re.match(r"^\s*- name: ([A-Z][A-Z0-9_]*)\s*$", line)
        if m:
            current = m.group(1)
            continue
        if current is None:
            continue
        if re.match(r"^\s*valueFrom:", line):
            current = None
            continue
        m = re.match(r'^\s*value:\s*"(.*)"\s*$', line)
        if m:
            values[current] = m.group(1)
    return values


def parse_configmap_key_refs(path: Path) -> set[str]:
    """抓 `configMapKeyRef:` 后面 `key: X` 引用的 configmap 键。"""
    keys: set[str] = set()
    pending = False
    for line in path.read_text(encoding="utf-8").splitlines():
        if "configMapKeyRef:" in line:
            pending = True
            continue
        if not pending:
            continue
        m = re.match(r"^\s*key:\s*(\S+)\s*$", line)
        if m:
            keys.add(m.group(1))
            pending = False
    return keys


# 5f 扫描的对象：compose 原文里每一处 `${DATABASE_NAME:-...}`（含嵌在
# DUALRUN_PG_DSN 默认值里、以及 command: 实参里的那些）。
_DATABASE_NAME_EXPR = re.compile(r"\$\{DATABASE_NAME(?::-([^}]*))?\}")


def _strip_compose_comment_lines(text: str) -> str:
    """去掉整行注释 —— 注释里为了解释这件事本身要写出 `${DATABASE_NAME:-...}`，
    那是引文，不是配置。"""
    return "\n".join(
        line for line in text.splitlines() if not line.lstrip().startswith("#"))


def check_env_wiring(
    errors: list[str],
    compose_env: dict[str, dict[str, str]],
    compose_text: str,
    k8s_config_text: str,
    notes: list[str],
    k8s_retired: bool = False,
) -> None:
    """AUD-39 的检查 5~8：env 口径一致、可达、无占位符、死键不许回来。

    `k8s_retired=True` 时跳过全部 k8s 侧断言（清单已归档，没有对比对象）；
    compose 侧断言一条不减。跳过范围由 main 的 note 显式说明，避免静默失效。
    """
    cm_keys = {} if k8s_retired else parse_configmap_keys(k8s_config_text)

    # 5a) 应用侧必备键
    for svc in APP_ENV_SERVICES:
        env = compose_env.get(svc, {})
        for key in APP_DB_ENV_KEYS + ["DATABASE_PASSWORD"]:
            if key not in env:
                errors.append(
                    f"docker-compose.yml 的 {svc} 缺 {key} —— 应用读的是这个 env 名（AUD-39）")
    if not k8s_retired:
        for key in APP_DB_ENV_KEYS:
            if key not in cm_keys:
                errors.append(
                    f"deploy/k8s/configmap.yaml 缺 {key} —— 应用读的是这个 env 名；"
                    f"缺了它会静默回落到镜像内的 config/*.yaml（AUD-39）")

    # 5b) compose ↔ k8s 的**身份**字段必须相等；**地址不比**（2026-09-25 放宽）
    #
    # 数据库移到宿主机后：容器内必然是 host.docker.internal，k8s 里是 Service 名
    # postgres —— 两者必然不同。继续比 DATABASE_HOST / REDIS_URL 的 host 只会逼出
    # 一个恒假的断言，那比没有断言更糟（要么被无视，要么被「修」成错的）。
    # 保留的是「连过去之后必须对得上」的那部分：库名、用户名、端口。
    #
    # ⚠️ 这里的**逐服务循环**是破坏验证逼出来的（2026-09-25）：原实现只拿
    # `compose_env.get("analysis-service")` 去对 k8s，于是 **data-service 的
    # 库名/用户名漂移完全没人查** —— 把它改成 somebody 后检查器照旧全绿。
    # 护栏只守一半比不守更容易让人放心，所以改成对每个应用服务都比。
    for svc in APP_ENV_SERVICES:
        env = compose_env.get(svc, {})
        if k8s_retired:
            continue
        for key in IDENTITY_ENV_KEYS:
            raw = env.get(key)
            if raw is None:
                continue
            # 比的是**展开 default 后**的值：`${DATABASE_NAME:-tellurion}` 与
            # `tellurion` 语义相等，拿原文比会恒红（2026-11-12 实测 4 条）。
            c = _expand_compose_value(raw)
            k = cm_keys.get(key)
            if c is not None and k is not None and c != k:
                errors.append(
                    f"{svc} 的 {key} 不一致 —— compose {raw}（默认 {c}）vs "
                    f"k8s {k}（AUD-39）")

    # 5b-2) REDIS_URL 的端口比对于 2026-11-10 删除：该键已从 compose 与
    # configmap 移除（data 面退役后零读取点），比对对象不存在了 —— 留着就是
    # 空转的假护栏（与上文 LOOPBACK_ONLY 的教训同型）。

    # 5c) DB 身份不能与 config/*.yaml 漂移（库名/用户名对不上 = 连不到库）
    #
    # ⚠️ 同 5b：原实现循环 `name` 遍历两个服务、却固定拿
    # `compose_env.get("analysis-service")` 当对比值 —— 于是 data-service
    # 的 yaml 永远在跟 **analysis-service 的 env** 比，而不是跟它自己的。
    # 现在各比各的。破坏验证（2026-09-25）验证过改坏能被抓到。
    #
    # 2026-11-11：循环清单收窄到 CONFIG_YAML_SERVICES —— core 只剩
    # analysis-service 这一份自带 YAML；ai-service 的 YAML 归 agent 仓，见 5c-2。
    for name in CONFIG_YAML_SERVICES:
        text = (ROOT / "config" / f"{name}.yaml").read_text(encoding="utf-8")
        for yaml_key, env_key in (("user", "DATABASE_USER"),
                                  ("database", "DATABASE_DATABASE")):
            value = find_nested_value(text, "database", yaml_key)
            want = _expand_compose_value(compose_env.get(name, {}).get(env_key))
            if value and want and value != want:
                errors.append(
                    f"config/{name}.yaml 的 database.{yaml_key}={value} 与它自己的部署 env "
                    f"{env_key}={want} 不一致 —— 应用会去连不存在的库/用户（AUD-39）")

    # 5c-2) ai-service 的 YAML 已不在本仓（AI 拆仓阶段 1）。
    #
    # 留着 core 那份不只是「冗余」：运行期没人读，却会被顺手改一下，然后与
    # agent 仓那份**静默**漂移 —— compose 起的 ai-service 镜像来自
    # `../tellurion-agent`，它读的是 agent 那份。所以两条：
    #   a) core 的 config/ai-service.yaml 不许回来；
    #   b) 同工作区存在 agent 仓时，对拍它的 yaml 与 compose 里 ai-service
    #      自己的 env（库名/用户名）。
    # 找不到 agent 仓时**明说**跳过：静默 skip 会让这条护栏在「只 checkout 单仓」
    # 的环境里无声失效，那比没有护栏更糟。
    if (ROOT / "config" / "ai-service.yaml").exists():
        errors.append(
            "本仓 config/ai-service.yaml 又出现了 —— AI 拆仓后它的唯一读者与真相都在 "
            "tellurion-agent/config/ai-service.yaml；本仓留一份会与那份静默漂移（AUD-39）")
    agent_yaml = ROOT.parent / "tellurion-agent" / "config" / "ai-service.yaml"
    if agent_yaml.is_file():
        text = agent_yaml.read_text(encoding="utf-8")
        for yaml_key, env_key in (("user", "DATABASE_USER"),
                                  ("database", "DATABASE_DATABASE")):
            value = find_nested_value(text, "database", yaml_key)
            want = _expand_compose_value(compose_env.get("ai-service", {}).get(env_key))
            if value and want and value != want:
                errors.append(
                    f"tellurion-agent/config/ai-service.yaml 的 database.{yaml_key}={value} "
                    f"与 docker-compose.yml 的 ai-service env {env_key}={want} 不一致 —— "
                    f"compose 起的 ai-service 会去连不存在的库/用户（AUD-39）")
    else:
        notes.append(
            f"未找到 {agent_yaml} —— 跳过 ai-service 的 config↔compose 身份对拍"
            f"（同工作区没有 agent 仓时判不了）")

    # 5d) 密码只能有**一个**变量（2026-09-25 改写）。
    #
    # 原文守的是「compose 的 postgres 服务与应用侧读同一个 ${DATABASE_PASSWORD}」。
    # postgres 服务已移出 compose，那句话失去了对象 —— 不断言就等于静默丢了一条
    # 护栏。新形态下照着写：**应用侧**（compose 的两个服务）都必须从
    # DATABASE_PASSWORD 取密码，而不是内联字面量。
    #
    # k8s 侧不在此列：configmap 里本来就没有密码（它在 Secret 里），由
    # postgres-statefulset 与两个 deployment 各自 configMapKeyRef/secretKeyRef 取。
    for svc in ("analysis-service",):
        value = compose_env.get(svc, {}).get("DATABASE_PASSWORD", "")
        if "DATABASE_PASSWORD" not in value:
            errors.append(
                f"docker-compose.yml 的 {svc} 的 DATABASE_PASSWORD 取的是 "
                f"{value or '(缺失)'} —— 必须引用同一个变量 DATABASE_PASSWORD，"
                f"否则改密码时各处不一致（AUD-39）")
    # data-service 不在这条循环里：它没有 DATABASE_PASSWORD 这个 env 键，密码在
    # DATABASE_URL 的插值里 —— 由 5e 断言那一段引用的仍是 ${DATABASE_PASSWORD}。

    # 5e) data-service 的 DSN 口径（2026-11-10）
    #
    # 数据服务只读一个 DATABASE_URL（+ 一个 DATA_MARKET_SCHEMA），而这两个值在
    # compose 与 k8s 里是**两种形态**：compose 是环境变量插值，k8s 是
    # `$(VAR)` 拼串。两边都能起得来，所以漏一半是静默失败 —— 这个检查把它们
    # 各自的形式钉住：
    #   - compose 的 DSN 密码段必须来自 ${DATABASE_PASSWORD}（地址段由 3b 断言
    #     必须是 host.docker.internal）；
    #   - k8s 的 DSN 必须由五个身份/凭据字段拼出（缺一项就拼出一个连不上的库，
    #     而 YAML 语法完全合法）；
    #   - DATA_MARKET_SCHEMA 两边同值且非空（空值回落到 public，与 market.* 全错开）。
    data_env = compose_env.get("data-service", {})
    compose_dsn = data_env.get("DATABASE_URL", "")
    if compose_dsn and "${DATABASE_PASSWORD" not in compose_dsn:
        errors.append(
            "docker-compose.yml 的 data-service 的 DATABASE_URL 没有引用 "
            "${DATABASE_PASSWORD} —— 密码必须只有一个变量，否则改密码时这一处"
            "静默失配（AUD-39）")

    compose_schema = data_env.get("DATA_MARKET_SCHEMA", "")
    if k8s_retired:
        if not compose_schema:
            errors.append(
                f"docker-compose.yml 的 data-service 缺 DATA_MARKET_SCHEMA —— 数据"
                f"服务回落 public 后与 market.* 的表全部错开，症状是「表不存在」"
                f"而不是配置错误")
    else:
        k8s_schema = cm_keys.get("DATA_MARKET_SCHEMA", "")
        if not compose_schema or not k8s_schema:
            errors.append(
                f"DATA_MARKET_SCHEMA 缺失 —— compose {compose_schema or '(缺失)'} vs "
                f"k8s {k8s_schema or '(缺失)'}；数据服务回落 public 后与 market.* 的表"
                f"全部错开，症状是「表不存在」而不是配置错误")
        elif compose_schema != k8s_schema:
            errors.append(
                f"DATA_MARKET_SCHEMA 不一致 —— compose {compose_schema} vs k8s {k8s_schema}")

        k8s_dsn = parse_deployment_env_values(k8s_manifest("data-deployment.yaml")).get(
            "DATABASE_URL", "")
        if not k8s_dsn:
            errors.append(
                "deploy/k8s/data-deployment.yaml 缺字面量 DATABASE_URL —— 数据服务的镜像"
                "只读这一个 DSN，没有它起来后连不到库（configmap 的 DATABASE_* 是给 "
                "analysis / ai 用的，不是给它的）")
        else:
            for placeholder in ("$(DATABASE_USER)", "$(DATABASE_PASSWORD)",
                                "$(DATABASE_HOST)", "$(DATABASE_PORT)",
                                "$(DATABASE_DATABASE)"):
                if placeholder not in k8s_dsn:
                    errors.append(
                        f"deploy/k8s/data-deployment.yaml 的 DATABASE_URL 缺 "
                        f"{placeholder} —— DSN 必须由 configmap/Secret 的身份字段拼出，"
                        f"写死值会在改密码/换库时静默失配")

    # 5f) `${DATABASE_NAME:-...}` 的默认值必须全仓同一个，且与 k8s 的库名同值
    #
    # 5b/5c 只比 analysis / ai 两个服务的 DATABASE_DATABASE，可 compose 里引用
    # 库名的实测有 10 处：data-service 的 DSN、analysis / ai / worker / dualrun
    # 的 DATABASE_DATABASE、full profile 四个跨仓服务的 DSN，以及 dualrun 的
    # -pg 实参（在 command: 里，5b 的 environment 解析结构根本看不到它）。
    #
    # 除了前两处，其余都没被比值断言覆盖：把任意一处 `:-tellurion` 改成别的
    # 库名，检查器照旧全绿，症状是「某个容器说表不存在」而没人知道是哪份配置
    # 错了。展开 default 只解决「比不出来」，这一项才提供「比」。
    exprs = _DATABASE_NAME_EXPR.findall(_strip_compose_comment_lines(compose_text))
    defaults = sorted({d.strip() for d in exprs})
    k8s_db = "" if k8s_retired else cm_keys.get("DATABASE_DATABASE", "")
    if not exprs:
        # 库名写死也合法，但那意味着这一项失去对象 —— 明说，不静默失效
        # （同 5b-2 / 3a 的教训：检查对象没了，脚本照常打勾就是假护栏）。
        notes.append(
            "docker-compose.yml 里已没有 ${DATABASE_NAME:-...} —— 库名口径改为"
            "字面量，5f 的默认值一致性检查无对象可查")
    elif "" in defaults:
        errors.append(
            "docker-compose.yml 的 ${DATABASE_NAME} 没有默认值 —— .env 未设时"
            "compose 会插值出空库名，容器连到一个不存在的库")
    elif len(defaults) > 1:
        errors.append(
            f"docker-compose.yml 的 ${{DATABASE_NAME:-...}} 默认值有 {len(defaults)} 种："
            f"{', '.join(defaults)} —— 各处容器会连到不同的库（5b/5c 只覆盖 "
            f"analysis/ai 两处，其余位置没人比）")
    elif k8s_db and defaults[0] != k8s_db:
        errors.append(
            f"docker-compose.yml 的 ${{DATABASE_NAME:-...}} 默认值是 {defaults[0]}，"
            f"而 deploy/k8s/configmap.yaml 的 DATABASE_DATABASE 是 {k8s_db} —— "
            f"两条部署路径连的不是同一个库（AUD-39）")

    # 6) env 可达性：注入的每个 env 名都要有读取点
    names, prefixes = derive_readable_env_names()
    if not k8s_retired:
        for key in sorted(cm_keys):
            if not is_readable(key, names, prefixes):
                errors.append(
                    f"deploy/k8s/configmap.yaml 的 {key} 全仓没有任何读取点 —— "
                    f"要么接上读取方，要么删掉（AUD-39）")
        for name in ("analysis-deployment.yaml", "data-deployment.yaml"):
            mf = k8s_manifest(name)
            for env_name in sorted(parse_deployment_env_names(mf)):
                if not is_readable(env_name, names, prefixes):
                    errors.append(
                        f"{mf.relative_to(ROOT).as_posix()} 注入的 {env_name} "
                        f"全仓没有任何读取点（AUD-39）")

        # 6b) configMapKeyRef 不能悬空
        for f in active_k8s_manifests():
            for ref in sorted(parse_configmap_key_refs(f)):
                if ref not in cm_keys:
                    errors.append(
                        f"deploy/k8s/{f.name} 引用了 configmap 键 {ref}，但 "
                        f"deploy/k8s/configmap.yaml 里没有这个键（AUD-39）")

    # 7) config/*.yaml 不得出现 ${...}
    #
    # 只看 `#` 之前的部分：注释里为了说明这件事，本身就要写出 `${...}` 这个
    # 形状（否则没法解释为什么它是陷阱）—— 那是引文，不是配置。
    for f in sorted((ROOT / "config").glob("*.yaml")):
        for lineno, line in enumerate(f.read_text(encoding="utf-8").splitlines(), 1):
            code = line.split("#", 1)[0]
            if "${" in code:
                errors.append(
                    f"config/{f.name}:{lineno} 出现 ${{...}} —— 本仓没有 env 展开器"
                    f"（无 os.ExpandEnv / envsubst），它不是模板，是字面量（AUD-39）")

    # 8) 负向钉：死键不许回来
    # (标签, env 名 → 值, 是否 postgres 容器自身的 env)
    surfaces: list[tuple[str, dict[str, str], bool]] = []
    for svc, env in sorted(compose_env.items()):
        surfaces.append((f"docker-compose.yml 的 {svc} 服务", env, svc == "postgres"))
    if not k8s_retired:
        surfaces.append(("deploy/k8s/configmap.yaml", cm_keys, False))
        for f in active_k8s_manifests():
            surfaces.append((
                f"deploy/k8s/{f.name}",
                {n: "" for n in parse_deployment_env_names(f)},
                f.name == "postgres-statefulset.yaml",
            ))
    for where, env, is_pg_container in surfaces:
        for banned, why in sorted(BANNED_ENV_NAMES.items()):
            if banned in env:
                errors.append(f"{where} 出现 {banned} —— {why}（AUD-39）")
        if is_pg_container:
            continue
        for name in sorted(POSTGRES_CONTAINER_ENV):
            if name in env:
                errors.append(
                    f"{where} 出现 {name} —— 那是 postgres 容器自己的约定名，"
                    f"应用侧一律用 DATABASE_*（AUD-39）")


# ── AUD-48：Dockerfile 的 COPY 源路径必须存在 ──────────────────────────
#
# 为什么需要这一项：**编译期没有任何东西检查 Dockerfile 里 COPY 的源路径。**
# `go build` / `go vet` / `gofmt` / `check_doc_links.py` 都不读 Dockerfile，
# 而 CI 又**不构建镜像** —— 于是「删掉一个目录但忘了删引用它的 COPY」会长期
# 静默，症状不是「构建失败」，而是**那个服务从来没起来过，而且看起来没人动过它**。
#
# 已两次同型（第三次就得有护栏）：
#   1. ODR-021 删 `cmd/risk` `cmd/execution` → Dockerfile.service 的构建行没跟着改
#      （该处注释里记着「从服务合并那天起就没能构建成功过」）
#   2. AUD-33 删 `cmd/analysis/static/` → 两处 `COPY cmd/analysis/static` 没跟着删，
#      2026-09-22 完整部署时炸 `failed to calculate checksum: not found`
#
# 边界（写在这里，免得下次误报时不知道该改哪里）：
#   - **只检查不带 `--from` 的 COPY**。带 `--from` 的源路径是**另一个构建阶段内部**
#     的路径（镜像内的 /app/...），不是 build context 里的路径，静态检查不了。
#   - build context 假定为**仓库根**（compose 的 `context: .` 与 Makefile 的
#     `docker build -f cmd/analysis/Dockerfile .` 都是根）。若将来某个 Dockerfile
#     改用别的 context，这一项会误报 —— 届时给它单独记 context。
#   - 支持通配符（`COPY go.mod go.sum* ./`）与多源（`COPY a b dst`）。
#   - 含 `${...}` 的源路径跳过：本仓没有 env 展开器，但那属于另一项检查的领地。

def iter_dockerfiles() -> list[Path]:
    files = sorted(ROOT.glob("Dockerfile*"))
    files += sorted(ROOT.glob("cmd/*/Dockerfile"))
    return [p for p in files if p.is_file()]


def check_dockerfile_copy_sources(errors: list[str]) -> None:
    for df in iter_dockerfiles():
        rel = df.relative_to(ROOT).as_posix()
        for lineno, raw in enumerate(
                df.read_text(encoding="utf-8").splitlines(), start=1):
            line = raw.strip()
            if not line.startswith("COPY"):
                continue
            tokens = line.split()[1:]
            # flags：--from=builder / --chown=... 一律跳过（--from 的源不在 context 里）
            while tokens and tokens[0].startswith("--"):
                if tokens[0].startswith("--from"):
                    tokens = []
                    break
                tokens = tokens[1:]
            if len(tokens) < 2:
                continue
            for src in tokens[:-1]:
                if "$" in src or src.startswith("/"):
                    continue
                if src in {".", "..", "./", "../"}:
                    # `COPY . .` —— 整个 context，必然存在（且 pathlib.glob 不接受 '.'）
                    continue
                if any(ch in src for ch in "*?["):
                    matches = sorted(ROOT.glob(src))
                else:
                    matches = [ROOT / src] if (ROOT / src).exists() else []
                if not matches:
                    errors.append(
                        f"{rel}:{lineno} 的 `COPY {src}` 源路径不存在 —— "
                        f"删目录时要连引用它的 COPY 一起删；docker build 只在最后一步才炸，"
                        f"而 CI 不构建镜像，所以会静默很久（AUD-48）")


# ── CORE-CI-IMAGE-GATE：compose 的 build 面（静态半边）─────────────────
#
# 背景（ODR-072 / W5-F5）：本仓 CI 此前**零镜像构建**（.github/workflows 里
# `docker` 零命中），于是「compose 声明的镜像到底出不出得来」没有任何判定者。
# AUD-48 只守 COPY 的**源路径**，守不住 `RUN go build ./cmd/xxx`：目录被删、
# 构建行没删，编译期不检查，docker build 才炸。Dockerfile.service 历史上正是
# 这样静默坏过两次（ODR-021 删 cmd/risk / cmd/execution；AUD-33 删 static）。
#
# 本项与 CI 的 `image-build` job 是同一件事的两半：
#   - 静态（本项）：build 目标目录存在、Dockerfile 存在、COPY 源存在；
#   - 动态（CI）：`docker build -f Dockerfile.service .` 真跑 → 镜像出得来。
# 静态检查读不懂 go toolchain 的包解析，只有真构建能证明镜像可用；真构建只在
# CI 跑，本地四条门禁靠本项兜底。

_SERVICE_KEY_RE = re.compile(r"^  ([A-Za-z0-9_.-]+):\s*$")
_BUILD_KEY_RE = re.compile(r"^    build:\s*$")
_BUILD_CONTEXT_RE = re.compile(r"^      context:\s*(\S+)\s*$")
_BUILD_DOCKERFILE_RE = re.compile(r"^      dockerfile:\s*(\S+)\s*$")


def parse_compose_builds(text: str) -> list[tuple[str, str, str]]:
    """抓 docker-compose.yml 的 build 面。

    返回 [(服务名, context, dockerfile)]；缺省 dockerfile 按 compose 语义补
    `Dockerfile`。只用标准库（本环境没有 pyyaml），靠缩进状态机抓规整块。
    """
    builds: list[tuple[str, str, str]] = []
    service: str | None = None
    in_build = False
    context: str | None = None
    dockerfile: str | None = None

    def flush() -> None:
        nonlocal context, dockerfile
        if in_build and service and context:
            builds.append((service, context, dockerfile or "Dockerfile"))
        context = dockerfile = None

    for raw in text.splitlines():
        line = raw.rstrip()
        if not line or line.lstrip().startswith("#"):
            continue
        m = _SERVICE_KEY_RE.match(line)
        if m:
            flush()
            in_build = False
            service = m.group(1)
            continue
        if _BUILD_KEY_RE.match(line):
            flush()
            in_build = True
            continue
        if not in_build:
            continue
        m = _BUILD_CONTEXT_RE.match(line)
        if m:
            context = m.group(1)
            continue
        m = _BUILD_DOCKERFILE_RE.match(line)
        if m:
            dockerfile = m.group(1)
            continue
        # build 块里出现别的键（缩进回到 4）或其下别的字段 ⇒ 块结束
        if not line.startswith("      "):
            flush()
            in_build = False

    flush()
    return builds


def check_compose_build_contexts(
    errors: list[str], notes: list[str], compose_text: str
) -> None:
    for svc, context, dockerfile in parse_compose_builds(compose_text):
        ctx_path = (ROOT / context).resolve()
        if not ctx_path.is_relative_to(ROOT):
            notes.append(
                f"{svc}: build context {context} 不在本仓 —— 镜像由该仓 CI 构建，"
                f"本仓不代构建也不断言")
            continue
        if not ctx_path.is_dir():
            errors.append(
                f"docker-compose.yml 的服务 {svc} 的 build context {context} 不存在"
                f"（CORE-CI-IMAGE-GATE）")
            continue
        df_path = ctx_path / dockerfile
        if not df_path.is_file():
            errors.append(
                f"docker-compose.yml 的服务 {svc} 用 {context}/{dockerfile} 构建，"
                f"但该 Dockerfile 不存在 —— compose 语法合法、只有真构建才炸；"
                f"CI 的 image-build job 与本地门禁都会红（CORE-CI-IMAGE-GATE）")


_GO_BUILD_CMD_RE = re.compile(r"\./cmd/[A-Za-z0-9_./-]*[A-Za-z0-9_-]")


def _dockerfile_logical_lines(text: str) -> list[tuple[int, str]]:
    """把 `\\` 续行合成逻辑行，返回 [(起始行号, 内容)]。

    行号取逻辑行的**首行**，`RUN go build ...` 的续行因此能报到正确位置。
    """
    out: list[tuple[int, str]] = []
    buf = ""
    start = 0
    for lineno, raw in enumerate(text.splitlines(), start=1):
        line = raw.rstrip()
        if not buf:
            start = lineno
        if line.endswith("\\"):
            buf += line[:-1] + " "
            continue
        buf += line
        out.append((start, buf))
        buf = ""
    if buf:
        out.append((start, buf))
    return out


def check_dockerfile_build_targets(errors: list[str]) -> None:
    for df in iter_dockerfiles():
        rel = df.relative_to(ROOT).as_posix()
        for lineno, line in _dockerfile_logical_lines(
                df.read_text(encoding="utf-8")):
            stripped = line.strip()
            if not stripped.startswith("RUN") or "go build" not in stripped:
                continue
            for token in _GO_BUILD_CMD_RE.findall(stripped):
                if not (ROOT / token).exists():
                    errors.append(
                        f"{rel}:{lineno} 的 `go build {token}` 目标不存在 —— "
                        f"删 cmd 目录时必须同步删这条构建行；编译期不检查，"
                        f"CI 镜像构建会红，没人构建时则永久静默"
                        f"（CORE-CI-IMAGE-GATE / AUD-48 同型）")



# ── AUD-13（2026-09-25 改写）：基础设施必须真在仓外，且容器显式指向它 ──────
#
# 这一类失败全是**静默**的：compose 语法合法、容器也能起来，只在第一次连库时
# 失败。而「Docker Desktop 不稳定 → 把数据库挪到宿主机」这个决定本身，如果
# compose 里还留着 postgres / redis 服务，就会变成两个进程抢同一个端口 ——
# 表现为「有时连得上、有时连错库」，比干脆连不上更难查。

def check_infra_is_external(
    errors: list[str],
    compose_env: dict[str, dict[str, str]],
    compose_bindings: dict[str, list[tuple[str | None, int]]],
) -> None:
    """检查 3：数据库/缓存不得由 compose 托管，且应用容器必须指向宿主机。"""
    # 3a) compose 里不得再有 postgres / redis 服务
    for svc in NATIVE_DB_DEPS:
        if svc in compose_env or svc in compose_bindings:
            errors.append(
                f"docker-compose.yml 仍有 {svc} 服务 —— 数据库/缓存已改为宿主机原生"
                f"安装（见文件头），容器版会与原生进程抢同一个端口（AUD-13）")

    # 3b) 应用服务必须显式注入宿主机地址，且值必须指向宿主机
    for svc, required in sorted(INFRA_ENV_REQUIRED.items()):
        env = compose_env.get(svc, {})
        for key, kind in required:
            value = env.get(key)
            if value is None:
                errors.append(
                    f"docker-compose.yml 的 {svc} 缺 {key} —— config/*.yaml 现在存的是"
                    f"宿主机视角（localhost），漏注入会让容器回落到 localhost 并连到"
                    f"容器自己（AUD-13）")
                continue
            if kind == "host" and value != CONTAINER_HOST_ALIAS:
                errors.append(
                    f"docker-compose.yml 的 {svc} 的 {key}={value} —— 容器内必须指向 "
                    f"{CONTAINER_HOST_ALIAS}（容器里的 localhost 是容器自己）（AUD-13）")
            if kind == "url" and CONTAINER_HOST_ALIAS not in value:
                errors.append(
                    f"docker-compose.yml 的 {svc} 的 {key}={value} —— URL 里必须出现 "
                    f"{CONTAINER_HOST_ALIAS}（容器里的 localhost 是容器自己）（AUD-13）")


# ── 3c) open-access ⟹ 所有端口映射必须回环（2026-09-25，AUD-52 的栈侧一半）──
#
# 背景（2026-11-12 修正）：应用层三态启动闸（pkg/bootstrap.DecideAuthStartup）
# **不看绑定地址** —— 容器必须绑 0.0.0.0，第 3 态（显式声明放开）在任何绑定下
# 都允许启动，风险只由启动 WARN 披露。于是「不可从其他主机到达」这条不变量
# 完全落在**发布层**，保证由这里（静态）+ tools/local-stack.sh（运行时 netstat）
# 给。AUTH_INSECURE_EXPOSURE=loopback-published 只是 operator 对发布范围的
# 声明，**声明本身不提供任何保证** —— 少了这两处校验，它就变成一句没人核对
# 的口头承诺（真护栏 vs 假护栏的分界）。
#
# **双向**是有意的（只写一个方向会漏掉一半）：
#   正向：声明了 open-access → 每条映射都必须回环（否则下单接口开给局域网）
#   反向：有非回环映射 → 必须有 JWT_SECRET 且不许开 open-access
# 破坏验证（2026-09-25）：把任意一条映射的 127.0.0.1: 前缀删掉 → 正向报错；
# 把 AUTH_INSECURE 改成 false 而映射仍回环 → 仍然合法（auth 开 + 回环没事）。

_AUTH_TRUE_VALUES = {"true", "1", "yes", "on"}


def _expr_default(inner: str) -> str:
    """从 `${...}` 的内部文本取「静态可判的默认值」。

    ${NAME:-DEF} → DEF；${NAME} / ${NAME:?msg} → ""（值取决于 .env，静态不可知，
    fail closed）。变量名里不可能含 ':'，所以第一个 ":-" 就是分隔符。
    """
    if ":-" in inner:
        return inner.split(":-", 1)[1]
    return ""


def _expand_compose_value(value: str | None) -> str:
    """把 compose 插值 `${NAME}` / `${NAME:-default}` 展开成「默认情况下会生效的值」。

    只看字符串形态，不读 .env（静态脚本读不到运行时环境）。语义：
      ${NAME:-DEF}  → DEF（.env 没设时的值）
      ${NAME}       → ""（值取决于 .env，静态不可知 → 按空处理，fail closed）
      ${NAME:?msg}  → ""（同上；缺值时 compose 自己会报错，不由本脚本负责）
      其余文本      → 原样保留

    **只用于比对，不用于读配置**：`${DATABASE_PASSWORD}` 这类「必须引用某变量」
    的断言要看原文，展开后就看不到引用了。

    花括号按层配对，而不是正则到第一个 `}`：`DUALRUN_PG_DSN` 的默认值里嵌着
    `${DATABASE_NAME:-tellurion}`，按第一个 `}` 截断会展开出一个「语法正确、
    值错误」的 DSN —— 那正是本检查要防的静默漂移。
    """
    if not value:
        return ""
    s = value.strip()
    out: list[str] = []
    i = 0
    while i < len(s):
        if not s.startswith("${", i):
            out.append(s[i])
            i += 1
            continue
        depth, j = 1, i + 2
        while j < len(s) and depth:
            if s.startswith("${", j):
                depth += 1
                j += 2
                continue
            if s[j] == "}":
                depth -= 1
            j += 1
        if depth:  # 未闭合：原样留下，由 5f 的残留断言报出来
            out.append(s[i:])
            break
        out.append(_expand_compose_value(_expr_default(s[i + 2:j - 1])))
        i = j
    return "".join(out)


def _is_loopback_bind(bind: str | None) -> bool:
    """bind=None 表示映射没写宿主地址 → 等价绑 0.0.0.0。"""
    if bind is None:
        return False
    b = bind.strip().strip("[]")
    if b in {"localhost"}:
        return True
    if b == "::1":
        return True
    return b.startswith("127.")


def check_open_access_exposure(
    errors: list[str],
    compose_env: dict[str, dict[str, str]],
    compose_bindings: dict[str, list[tuple[str | None, int]]],
) -> None:
    """检查 3c：open-access 的声明与端口发布范围必须互相蕴含。"""
    open_access_services = [
        svc for svc, env in compose_env.items()
        if _expand_compose_value(env.get("AUTH_INSECURE")).lower() in _AUTH_TRUE_VALUES
    ]
    analysis_env = compose_env.get("analysis-service", {})
    secret = _expand_compose_value(analysis_env.get("JWT_SECRET"))
    exposure = _expand_compose_value(analysis_env.get("AUTH_INSECURE_EXPOSURE"))
    has_secret = bool(secret)

    # 收集所有非回环映射（含 bind=None 即 0.0.0.0）
    exposed: list[str] = []
    for svc, binds in sorted(compose_bindings.items()):
        for bind, host_port in binds:
            if not _is_loopback_bind(bind):
                exposed.append(f"{svc}:{host_port}（绑定 {bind or '0.0.0.0'}）")

    # 正向：声明了 open-access，就不许有任何非回环映射
    if open_access_services and exposed:
        errors.append(
            f"docker-compose.yml 开了 open-access（{', '.join(sorted(open_access_services))} "
            f"的 AUTH_INSECURE 为真）却存在非回环端口映射：{'; '.join(exposed)} —— "
            f"这等于把下单接口开给整个局域网。要么把映射改成 127.0.0.1: 前缀，"
            f"要么改回鉴权模式（AUD-52 / P0-4）")

    # 反向：有非回环映射就必须真的开了鉴权
    if exposed and not has_secret:
        errors.append(
            f"docker-compose.yml 存在非回环端口映射（{'; '.join(exposed)}）但没有 "
            f"JWT_SECRET —— 对外发布必须配鉴权（AUD-52 / P0-4）")

    # 反向-2：开了 open-access 就必须给容器形态的发布层声明。应用层已不再因
    # 绑定地址拒绝启动（第 3 态任何绑定都放开），所以这条是**本栈的一致性
    # 策略**：把「只发布到回环」写进配置，和检查 3c 的回环前缀一起构成可审计
    # 的声明；缺了它，启动 WARN 里的 exposure 会显示为空。
    if open_access_services and exposure != "loopback-published":
        errors.append(
            f"docker-compose.yml 开了 open-access 但 analysis-service 的 "
            f"AUTH_INSECURE_EXPOSURE 是 {exposure or '(缺失)'} —— 必须是 "
            f"loopback-published：本栈要求把「端口只发布到回环」写成显式声明"
            f"（AUD-52 / P0-4）")

    # 反向-3：同时给了密钥和 open-access 声明 → 密钥优先，声明变成误导。
    if open_access_services and has_secret:
        errors.append(
            "docker-compose.yml 同时给了 JWT_SECRET 与 AUTH_INSECURE=true —— "
            "pkg/bootstrap.DecideAuthStartup 里**密钥优先**，于是 AUTH_INSECURE 与 "
            "AUTH_INSECURE_EXPOSURE 都不生效，读配置的人会被误导成「这是 open-access」"
            "（AUD-52 / P0-4）")


def main() -> int:
    if not COMPOSE.exists():
        print(f"✗ 找不到 {COMPOSE}")
        return 1

    compose_text = COMPOSE.read_text(encoding="utf-8")
    compose_bindings = parse_compose_ports(COMPOSE)
    # 一致性比对只关心宿主端口（每个服务的第一个映射），绑定地址由检查 3 管。
    compose_svc = {
        svc: ports[0][1] for svc, ports in compose_bindings.items() if ports
    }
    errors: list[str] = []
    notes: list[str] = []

    k8s_retired = k8s_is_retired(errors, notes)
    k8s_svc = {} if k8s_retired else parse_k8s_ports()

    # 1) 两边都有的服务，端口必须一致（k8s 退役后没有「两边」）
    if not k8s_retired:
        for name, cport in sorted(compose_svc.items()):
            if name in IGNORED_SERVICES:
                continue
            kport = k8s_svc.get(name)
            if kport is None:
                reason = ALLOWED_MISSING_IN_K8S.get(name)
                if reason:
                    notes.append(f"{name}: k8s 未部署（{reason}）")
                else:
                    errors.append(
                        f"{name}: compose 里有（:{cport}）但 k8s 里没有对应 Service "
                        f"—— 是真漂移还是有意不部署？后者请加进 ALLOWED_MISSING_IN_K8S"
                    )
                continue
            if kport != cport:
                errors.append(f"{name}: 端口不一致 —— compose {cport} vs k8s {kport}")

    # 2) DATA_SERVICE_URL 两边必须一致，且指向的端口要对得上 data-service
    k8s_config_text = (
        "" if k8s_retired
        else k8s_manifest("configmap.yaml").read_text(encoding="utf-8"))
    compose_url = find_env_value(compose_text, "DATA_SERVICE_URL")
    k8s_url = None if k8s_retired else find_env_value(k8s_config_text, "DATA_SERVICE_URL")

    if compose_url is None:
        errors.append("docker-compose.yml 缺 DATA_SERVICE_URL")
    if not k8s_retired and k8s_url is None:
        errors.append("deploy/k8s/configmap.yaml 缺 DATA_SERVICE_URL —— 此前正是缺这一项，"
                      "全靠代码默认值碰巧能用（P1-8）")
    if compose_url and k8s_url and compose_url != k8s_url:
        errors.append(f"DATA_SERVICE_URL 不一致 —— compose {compose_url} vs k8s {k8s_url}")

    for where, url in (("compose", compose_url), ("k8s", k8s_url)):
        if not url:
            continue
        m = re.match(r"^https?://([^:/]+):(\d+)", url)
        if not m:
            errors.append(f"{where} 的 DATA_SERVICE_URL 格式不对：{url}")
            continue
        host, port = m.group(1), int(m.group(2))
        actual = compose_svc.get(host) if where == "compose" else k8s_svc.get(host)
        if actual is None:
            errors.append(f"{where}: DATA_SERVICE_URL 指向 {host}，但找不到这个服务")
        elif actual != port:
            errors.append(
                f"{where}: DATA_SERVICE_URL 指向 {host}:{port}，而 {host} 实际端口是 {actual}")

    # 3) 数据库 / 缓存必须真在仓外，且应用容器显式指向宿主机（AUD-13 改写）
    compose_env = parse_compose_environment(COMPOSE)
    check_infra_is_external(errors, compose_env, compose_bindings)

    # 3c) open-access 的声明与端口发布范围必须互相蕴含（AUD-52 / P0-4）
    check_open_access_exposure(errors, compose_env, compose_bindings)

    # 4) gin 的运行模式只能有一个来源：config/*.yaml 的 server.gin_mode（AUD-29）
    #
    # gin 自己在 init() 里读 GIN_MODE 环境变量（gin/mode.go:52），所以在部署配置里
    # 写 GIN_MODE 等于给同一个决定开第二个入口 —— 而且是**看不见**的那个：全仓
    # grep 不到任何读取点，只有 gin 的内部实现知道它存在。此前
    # deploy/k8s/configmap.yaml 就写着 GIN_MODE: "release"，它确实生效，但只在
    # k8s 生效（compose 没写）—— 两条部署路径的 gin mode 来源不同却不报错。
    for where, text in (("docker-compose.yml", compose_text),):
        if find_env_value(text, "GIN_MODE") is not None:
            errors.append(
                f"{where} 设置了 GIN_MODE —— gin mode 的唯一来源是 config/*.yaml 的 "
                f"server.gin_mode（AUD-29）；gin 会自己读 GIN_MODE，写在这里会绕过它")
    if not k8s_retired and find_env_value(k8s_config_text, "GIN_MODE") is not None:
        errors.append(
            "deploy/k8s/configmap.yaml 设置了 GIN_MODE —— gin mode 的唯一来源是 "
            "config/*.yaml 的 server.gin_mode（AUD-29）")

    # 5~8) env 口径（AUD-39）
    check_env_wiring(errors, compose_env, compose_text, k8s_config_text, notes,
                     k8s_retired=k8s_retired)

    # 9) Dockerfile 的 COPY 源路径必须存在（AUD-48）
    check_dockerfile_copy_sources(errors)

    # 10) compose 的 build 面必须落在真实 Dockerfile 上，且 Dockerfile 的
    #     `go build ./cmd/xxx` 目标目录还在（CORE-CI-IMAGE-GATE 静态半边）
    check_compose_build_contexts(errors, notes, compose_text)
    check_dockerfile_build_targets(errors)

    # 输出
    for n in notes:
        print(f"· {n}")
    if errors:
        print(f"\n✗ 部署配置漂移 {len(errors)} 处：")
        for e in errors:
            print(f"  - {e}")
        return 1

    if k8s_retired:
        print("✓ 部署配置一致（单一 docker-compose.yml；k8s 清单已退役，"
              "无跨编排对比对象）")
    else:
        print("✓ 部署配置一致（compose ↔ k8s：服务端口 + DATA_SERVICE_URL）")
    print("✓ 数据库已移出 compose，应用容器显式指向 host.docker.internal"
          "（缓存 REDIS_URL 2026-11-10 起无消费者，已不再断言）（AUD-13 改写）")
    print("✓ open-access 与端口发布范围互相蕴含（AUTH_INSECURE ⟺ 所有映射回环；"
          "对外发布 ⟹ 有 JWT_SECRET）（AUD-52 / P0-4）")
    print("✓ gin mode 无 GIN_MODE 旁路（唯一来源 server.gin_mode）")
    if k8s_retired:
        print("✓ env 口径一致（compose 侧：必备键、身份字段、DSN 密码引用、"
              "库名默认值 —— k8s 已退役，不参与对比）")
        print("✓ 注入的每个 env 都有读取点（compose 侧）")
    else:
        print("✓ env 口径一致（compose ↔ k8s 的库名/用户名/端口相同 —— 先把 "
              "${VAR:-default} 展开再比；地址按环境各写一份，刻意不比；"
              "config/analysis-service.yaml 与 compose 一致，agent 仓的 "
              "ai-service.yaml 在工作区时同样对拍）")
        print("✓ compose 每处 ${DATABASE_NAME:-...} 默认值相同，且与 configmap 的 "
              "DATABASE_DATABASE 同值")
        print("✓ 注入的每个 env 都有读取点（configmap 键 + 应用 deployment 的 env）")
    print("✓ config/*.yaml 无 ${...} 占位符（本仓没有 env 展开器）")
    print("✓ 死键负向钉（POSTGRES_HOST/PORT、REDIS_HOST/PORT、"
          "REDIS_PASSWORD、DB_PASSWORD 均未出现）")
    print("✓ Dockerfile 的 COPY 源路径都存在（删目录时别落下引用它的 COPY）")
    print("✓ compose 的 build 面都指向本仓真实 Dockerfile，且 Dockerfile 的 "
          "`go build ./cmd/xxx` 目标目录存在（CORE-CI-IMAGE-GATE 静态半边；"
          "动态半边 = CI image-build job 真跑 docker build）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
