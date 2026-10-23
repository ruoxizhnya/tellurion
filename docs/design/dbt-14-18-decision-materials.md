# DBT-14 ~ DBT-18 裁决材料（裁-05 ~ 裁-09）

- **状态**：⬜ 材料，**未裁决**。本文件不含代码改动、不含任何数据库写操作。
- **编制**：2026-10-24。
- **编号说明**：派发原文指向「第 5 节 裁-05～裁-09」，该节不在本仓可检索范围内。本文件按任务顺序把 **DBT-14→裁-05、DBT-15→裁-06、DBT-16→裁-07、DBT-17→裁-08、DBT-18→裁-09** 一一对应；若原编号含义不同，以主任裁决为准。
- **共同边界**：五单都**不做代码施工**；DBT-15 **不做任何 `DELETE` / `TRUNCATE`**；DBT-17 若接因子，因与 `pkg/data` 其它单文件重叠，**必须先报协调者排期**；DBT-18 主体在 `tellurion-agent`，core 只留接口与指针。

---

## 裁-05 / DBT-14 · EquityDeep `fetched_at` 契约漂移

### 待裁事项
`fetched_at` 到底按哪种形状算合规：契约声明的 `date-time`，还是实现只接受的 `YYYY-MM-DD`？

### 证据
- 契约：[snapshot.schema.json](file:///c:/Users/ruoxi/Workspace/tellurion/contracts/snapshot.schema.json) 中 `fetched_at` 为 `"type": "string", "format": "date-time"`；`report_period` / `ann_date` 是 `format: date`。
- 实现：[equitydeep.go](file:///c:/Users/ruoxi/Workspace/tellurion/pkg/data/equitydeep/equitydeep.go#L96-L111) 的 `parseContractDate` 只用 `snapshotDateLayout = "2006-01-02"`，错误文案 `%s must be YYYY-MM-DD`。
- 现状钉子（DBT-11 所加，未改任何一边）：
  - `TestNormalize_ContractDateLayoutIsCalendarDateOnly`
  - `TestNormalize_MissingFetchedAtOnBothSidesIsAnError`
  - `TestNormalize_SourceAndFetchedAtErrorOrder`
  - 反例表里 `"06-05-2026"` 期望 `fetched_at must be YYYY-MM-DD`。
- 影响面：严格按契约发 `date-time` 的快照，整条请求 400；PIT 锚点是 `ann_date`，`fetched_at` 只承担「同键多版本排序位」。

### 选项
| 选项 | 做法 | 代价 |
|---|---|---|
| A | 改实现，只吃 RFC3339 `date-time` | 现有 `date` 生产者全部 400，破坏面最大 |
| B | 改契约声明为 `format: date` | 实现不动，但同一天内的 restatement 失去排序位，违背 `fetched_at` 的契约用途 |
| C | 两者都吃（RFC3339 优先，`YYYY-MM-DD` 兼容），落库统一归一为 UTC | 需要明确「无时区偏移的 date-time」如何解释；测试与 SPEC 需同步 |

### 推荐
**C**。理由：`ann_date` 才是 PIT 硬锚点，`fetched_at` 的语义是版本排序；只收日历日会把同日多次抓取压平，只收 `date-time` 又会打断现有生产者。C 同时保住两者，代价只是把「归一口径」写清楚（建议：无偏移的 `date-time` 按 UTC 解释，或直接拒绝并报错，二选一由主任定）。

### 裁决后施工清单（本轮不做）
1. 实现侧加 RFC3339 解析分支，`date` 仍兼容；归一为 UTC。
2. 契约侧若维持 `date-time`，在 `description` 里写明兼容 `date` 的过渡规则；若改 `format`，需双侧同步 PR + 递增 version。
3. 更新 `docs/SPEC.md` 的 EquityDeep 摄入契约节（citation/snapshot 形状）。
4. 测试：保留现有三颗钉子，新增「date-time 被接受」「非法形状被拒且原因可检索」；破坏验证按裁决后**不该接受的形状**发请求必须红。

### 边界
不动 `ann_date` 的 PIT 语义；不改 `field_dictionary.yaml` 白名单；不改 `pkg/domain` 字段（`spec_drift_test.go` 双向钉 SPEC）。

---

## 裁-06 / DBT-15 · `ingest.raw` 的 155 行探针假归档

### 待裁事项
这 155 行「明知是假」的归档 residue：授权删除，还是保留并成文承认？

### 证据（DBT-11 实测，2026-10-19；本轮只读材料，未复测、未动数据）
- `ingest.raw` 中 `payload ? 'probe' AND source='akshare'` = **155 行**；另有 **2 行** `source LIKE 'unit-test%'`。
- 测试自清理已于 2026-10-19 修好（`e23ddec`：按本 run 的 `content_hash` 精确删 + 读回复核）；**既有 residue 不是本轮产生**。
- 预期删后读数：**12,108 → 11,951**。
- DBT-11 的「`fina_indicator` 到底返回哪些字段」正是**纯读这张表**取证——脏 residue 已经实际污染过一次读数。

### 选项
| 选项 | 做法 | 代价 |
|---|---|---|
| A | 按可识别锚点精确删（两条 DELETE，禁止全表） | 动共享库，需主任授权；删后必须前后读数留证 |
| B | 保留，SPEC 成文标注为「演示 residue，读证据时需排除」 | 零风险，但每个读方都要记得加过滤条件，重复踩坑 |
| C | 保留并迁移到独立 schema/表 | 改动面最大，超出本单边界 |

### 推荐
**A**，但**必须由主任明确授权**（删别人写进共享库的行）。两条语句已备好：
```sql
DELETE FROM ingest.raw WHERE payload ? 'probe' AND source='akshare';
DELETE FROM ingest.raw WHERE source LIKE 'unit-test%';
```
执行前后各读一次 `count(*)`，并复核 `count(*) FILTER (WHERE payload ? 'probe') = 0`。

### 风险
- 共享库：执行窗口需与其它 agent 协调，避免与同步作业并发。
- 锚点误伤：两条语句都以「测试特征」为锚，不使用时间范围或全表条件。
- `tushare.*` 的 11,932 行真归档**绝不能碰**（证据 API 的返回源，DBT-11 读过）。

### 边界
**本轮不做 `DELETE` / `TRUNCATE`**；不改测试去绕过问题（测试侧已修）；其他表的测试残留只登记不处理。

---

## 裁-07 / DBT-16 · `fundamentals_detail` 的生产者

### 待裁事项
`fundamentals_detail` 的真实数据由谁产出？在产出方落实前，是「core 提供投递工具等数据」，还是「成文不可得 + 明确降级语义」？

### 证据（DBT-11 实测，2026-10-19；本轮未复测）
- 门与读方都验证过：真 HTTP 门 → 真库 → PIT 读回，`0 → 5 → 0` 行无残留。
- 缺的是**没人往门里投 ndjson**：`DecodeSnapshot` 的生产唯一调用点在 [handlers_equitydeep_ingest.go](file:///c:/Users/ruoxi/Workspace/tellurion/cmd/data/handlers_equitydeep_ingest.go#L116)，等的是外部 EquityDeep 产出。
- Tushare 免费权限给不出所需字段：`tushare.fina_indicator` 的 25 条 raw `fields` 只有 `[ts_code, ann_date, end_date, roe, roa, gross_margin]`；因子需要的 8 个字段（`total_revenue / operating_cost / contract_liability / ocf_net / net_profit_attr / total_assets / equity_attr / inventory`，见 [equitydeep.go](file:///c:/Users/ruoxi/Workspace/tellurion/pkg/data/equitydeep/equitydeep.go#L41-L49)）全部 miss。
- 拿不到时五个因子走 `ErrNoFundamentalsDetail`（[equitydeep.go](file:///c:/Users/ruoxi/Workspace/tellurion/pkg/data/equitydeep/equitydeep.go#L60-L70)）。
- **禁止用编造数据灌数**：那会让五个因子直接读假财务值。

### 选项
| 选项 | 做法 | 代价 |
|---|---|---|
| A | 产出方 = EquityDeep 数据底座（vault 研究投影），core 只做门 + 投递工具 | 需跨团队排期；依赖外部格式稳定 |
| B | 现在就在 core 加 `ingest-snapshot` 子命令（形状照 `mining`）等数据 | 工具先落地，但无数据仍验收不了「有人读得到」 |
| C | 成文「本仓可及源内不可得」，写清需要谁产出什么，并固化降级语义 | 零代码，但五个因子长期不可用 |

### 推荐
**A 的归属 + C 的短期口径**：先由主任确认产出方是 EquityDeep 侧；在产出方给出首批合法 snapshot 前，按 C 成文（SPEC + AGENTS §14 两处口径一致），`ErrNoFundamentalsDetail` 的降级行为写清。投递工具（B）可等产出方格式冻结后再立单，避免工具先于格式返工。

### 风险
- 用「迁就 Tushare」的方式改白名单 = 引入口径漂移，**禁止**。
- 不建议为凑验收而放宽 `ann_date` 硬要求（会引入 look-ahead）。

### 边界
不改字典白名单；不改因子数学定义（D3 冻结）；不在 core 实现 EquityDeep 研究逻辑（ADR-023）；不新增外部依赖不报批。

---

## 裁-08 / DBT-17 · `capital_flow` / `top_list` 零读方

### 待裁事项
两张已有真数据的表：接一个说得出口的用途（因子 / API / 前端），还是明确为「仅储备」并同步 cron 状态？

### 证据（2026-10-19 行数；2026-10-24 复跑 grep 读方现状）
- 行数（DBT-09/10 实测）：`capital_flow` 18 行、`top_list` 256 行。
- 读方 grep（本仓 `*.go`）：
  - `capital_flow`：只出现在执行器、测试、`internal/repoguard` 白名单与注释；**无 handler、无因子、无 SELECT 读方**。
  - `top_list`：同样只有执行器、作业创建、cron 注册与测试；**无独立 API 读端点、无因子**。
- cron 现状（[sync_handlers.go](file:///c:/Users/ruoxi/Workspace/tellurion/cmd/data/sync_handlers.go#L81-L91)）：`top_list` **在**名单，`capital_flow` **不在**。理由已写成注释：`top_list` 整市场按日翻页 ≈1–2 发请求；`capital_flow` 逐 symbol 一发，全池 ≈5900 发，免费档 200 req/min。
- 口径代价（DBT-09 已登记）：表内数值是 **Tushare 分档成交额**形态，`main_net` / 买卖额 / 两个 ratio / `retail_*` / `close_price` / `change_pct` 本源不给 ⇒ NULL；两族口径混在同表靠 `source` 列区分。**任何读方按东财含义解释前必须先读 `source`。**

### 选项
| 选项 | 做法 | 代价 |
|---|---|---|
| A | 接因子（横截面资金流/情绪因子），走 D3 冻结定义流程 + `factor_cache` | 价值最高，但与 `pkg/data` 其它单文件重叠，**需协调者排期** |
| B | 只加 API 读端点给人看 | 实现小，但仍需一个真实研究问题，否则是「为有数据造读方」 |
| C | 明确「仅储备」，`capital_flow` 维持不进 cron | 零施工；承认配额不值 |

### 推荐
- `capital_flow`：**C**（维持现状，明确储备）。零读方 + 逐 symbol 高配额，日更不成立。
- `top_list`：**A 的候选**，但先立因子定义单并报协调者排期；在因子落地前保持现有 cron（成本可忽略、数据可用）。
- 无论选哪个，SPEC 的数据缺口表必须写全两表的「行数 / 源 / 口径 / 读方现状」，NULL 列写清「缺失 ≠ 0」。

### 风险
- 把 NULL 当 0 会直接造出错因子；`source` 列不读就按东财口径解释 = 口径混用。
- 若选 A，文件重叠必须先排期，不能在本单顺手改 `pkg/data`。

### 边界
不为「有数据」而造读方；不改四契约签名（ADR-031）；不接新源；不做数据获取（DBT-09/10 已收口）。

---

## 裁-09 / DBT-18 · mining 的真实 Proposer 归属

### 待裁事项
`cmd/data mining` 的候选来源从 JSON 文件升级为真实 LLM/MCTS 生成器：归属 core 还是 `tellurion-agent`？

### 证据
- core 侧冻结契约：[pipeline.go](file:///c:/Users/ruoxi/Workspace/tellurion/pkg/mining/pipeline.go#L31-L36) 的 `Proposer interface { Propose(context.Context) ([]Candidate, error) }`。
- core 现状实现：[mining_cmd.go](file:///c:/Users/ruoxi/Workspace/tellurion/cmd/data/mining_cmd.go#L90-L98) 的 `fileProposer`（读 JSON 候选文件），装配在 `:345`。
- agent 侧已有候选生成原语：[proposer.go](file:///c:/Users/ruoxi/Workspace/tellurion-agent/pkg/ai/loop/proposer.go) 的 `RandomProposer` / `TPEProposer`，但签名是 `Suggest(observed []Observation) Suggestion`，**不是** `mining.Proposer`；`handlers_explore.go:232` 用它驱动参数探索控制器。
- 依赖方向：`tellurion-agent → core` 单向（repoguard 钉住），AI 层整体已迁往 agent（ADR-027 §5）。

### 选项
| 选项 | 做法 | 代价 |
|---|---|---|
| A | 归属 agent：core 只保留 `mining.Proposer` 冻结接口 + 台账指针 | 符合 ADR-023/ADR-027；agent 需新增一个适配 `mining.Proposer` 的实现与六字段单 |
| B | core 实现 LLM/MCTS | 违反「core 不引入 LLM SDK」「不在本仓实现第二个编排者」 |
| C | 双轨：core 保留 fileProposer，agent 另做 | 短期可行，但会产生两个候选来源口径 |

### 推荐
**A**。core 把 `fileProposer` 保持为测试/离线装配的最小实现即可，真实生成器在 agent 仓实现并把 `mining.Candidate` 作为输入契约消费；core 台账只留指针，不镜像正文。

### 风险
- agent 侧现有 `loop.Proposer` 与 `mining.Proposer` 不是同一接口；需要明确适配层放哪一侧（推荐 agent 侧适配，core 不改签名）。
- 若 agent 反向依赖 core 的 `pkg/mining`，需确认 repoguard 现有白名单是否允许该包（依赖方向本身不变）。

### 边界
core 不引入 LLM SDK；不在 core 实现第二个编排者；不改 `Pipeline` 签名。

---

## 汇总（供裁决）

| 裁决 | 对应单 | 推荐 | 是否触碰数据 | 是否需跨仓/协调 |
|---|---|---|---|---|
| 裁-05 | DBT-14 | C：实现两者都吃，落库归一 UTC | 否 | 契约仓/SPEC 同步 |
| 裁-06 | DBT-15 | A：授权后按锚点精确删 | **是（需授权）** | 共享库执行窗口协调 |
| 裁-07 | DBT-16 | A 归属 + C 短期口径 | 否（未来灌数才写） | EquityDeep 产出方 |
| 裁-08 | DBT-17 | `capital_flow`=C；`top_list`=A 候选 | 否 | 因子单需协调者排期 |
| 裁-09 | DBT-18 | A：归 `tellurion-agent` | 否 | agent 仓立六字段单 |

**未落项如实登记**：上述五项均**未施工**；本文件只提供裁决依据。
