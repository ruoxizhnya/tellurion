# QT-62 · 宿主区与模块静态数据重叠的可证性

> **性质**：实现 + 证据。本单只解决「重叠能不能被证明」，不改已冻结的 ABI 契约。
> **对应台账**：`docs/TASKS.md` QT-62。前置定则见 `adr-029-amendment-host-api-v2.md` §8。
> 实现日期：2026-10-22。

---

## 1. 立项证据（本单之前判不住的那条）

QT-01 ⑤ 的第二类坏 artifact 是「`__host_params_ptr` / `__host_scratch_ptr` 落在模块自己的静态数据上」。
今天只有一条静态判据：`MinHostParamsPtr = 4096`（`pkg/backtest/contracts/abi.go`）。

它对**低区**有效，因为 TinyGo 的 data/stack 在低地址；但数据段一旦越过 4096，
下面这个模块在旧实现里**完全合法**：

| 声明 | 值 |
|---|---|
| `__host_params_ptr` | 4096 |
| `__host_scratch_ptr` | 32768 |
| `__host_scratch_size` | 65536 |
| Data section 活动段 | `[32768, 32896)` |

四项检查（对齐 / 下界 / 区间内 / 不越界）一条都不踩，host 一写 scratch 就改掉策略变量 —— **静默错**。
这正是 N5 立项要防的形态。

---

## 2. 地址空间权威定则（先定，再判）

两段地址都由 artifact 自己声明，没有第三方可以仲裁「谁先谁后」：

| 角色 | 定义 | 来源 |
|---|---|---|
| 模块静态数据 | artifact 的 **Data section（id=11）活动段**的并集，区间半开 `[offset, offset+len)` | wasm 字节码 |
| 宿主区 | 三个导出 global 声明的 `params` / `scratch` 区间 | artifact 导出符号 |
| 判定 | 两者相交 ⇒ **拒绝 artifact**。不重排地址、不缩区间、不「尽力解释」 | 本文件 |

为什么不能靠运行时：wazero v1.12 的 `CompiledModule` 不暴露 Data section（实测接口只有
`Name` / `ImportedFunctions` / `ExportedFunctions` / `ImportedMemories` / `ExportedMemories` /
`CustomSections` / `Close`），instantiate 之后能读到的只有 global 值与 `Memory().Size()`。

**为什么选静态腿（方案 ①）而不是哨兵回读（方案 ②）**：哨兵腿测的是「这一次有没有踩到」，
依赖测试策略真的跑到那段代码；静态腿测的是「这个 artifact 有没有可能踩到」，在加载门处就能判，
且不需要执行不可信代码。行为腿保留为将来的补充证据，**不作为本单的判据**。

---

## 3. 实现

### 3.1 解析器（`pkg/sandbox/wasm/data_segments.go`）

`ParseStaticDataImage(wasmBytes) (contracts.StaticDataImage, error)`

- 只认 WebAssembly 1.0 三种数据段模式：
  - `0` 活动 / memory 0；
  - `2` 活动 / 显式 memory index（index ≠ 0 ⇒ 不可证明）；
  - `1` 被动（bulk-memory）⇒ **不可证明**（落点由运行期 `memory.init` 决定）。
- offset 表达式只认单个 `i32.const <非负 32 位> end`。
  - `global.get` ⇒ 不可证明（instantiate 前不知值）；
  - 其它 opcode / 负偏移 / 多指令 ⇒ 不可证明。
- 两种失败**分开报**：
  - `StaticDataImage.Unprovable` = 合法但证明不了 ⇒ 调用方 fail-closed；
  - `error` = 字节流读不下去（只可能来自解析器/编码器 bug）。
- `DataCount`（id=12）节**不单独下结论**：有它不代表有被动段，真正的被动段会以 `flag=1`
  出现在 Data section 里。凭「有 DataCount」就拒绝，会把合法使用 bulk memory 的 TinyGo
  产物一起打死 —— 假红比没护栏更糟。

### 3.2 判定（`pkg/backtest/contracts/abi.go`）

`HostRegionDeclaration.CheckAgainstStaticData(img StaticDataImage) error`

- `img.Unprovable != ""` ⇒ `ErrHostRegionDataUnprovable`；
- 任一段与 `params [ptr, ptr+MaxParamsBytes)` 或 `scratch [ptr, ptr+size)` 相交 ⇒
  `ErrHostRegionOverlapsData`，报错点名导出符号与两个区间；
- 零长段先摘掉（半开区间下 `[x,x)` 会被 `rangesOverlap` 判成相交，那是误报）；
- 地址算术用 `uint64`：`0xFFFFFFF8 + 64` 不会绕回低区造成误报。

### 3.3 接线（`ValidateLoadedArtifact`）

判定顺序即契约：

```
Compile ─► ValidateImports ─► DetectHostAbiFamily
Instantiate ─► ReadHostRegion ─► decl.Validate(memorySize)   ← 声明本身合法吗
            ─► decl.CheckAgainstStaticData(dataImage)        ← 声明踩到模块数据吗（QT-62）
            ─► Initialize
```

- **v1 不受影响**：legacy 布局没有宿主区声明，`ValidateLoadedArtifact` 在 v1 分支提前返回
  `legacy_host_layout` 诊断；`NewOnBarSession` 也只对 v2 解析数据段。
- 顺序刻意的：先报「声明不合法」，再报「声明合法但踩数据」，读者才分得清该改声明还是改数据布局。

---

## 4. 验收证据

### 4.1 立项腿（之前过不了，现在被拒）

`TestNewOnBarSession_V2ScratchPtrInsideHighDataSegment`（`pkg/sandbox/wasm/qt62_host_region_data_test.go`）

- 数据段 `[32768, 32896)` + `__host_scratch_ptr=32768` ⇒ `ErrHostRegionOverlapsData`，
  报错含 `__host_scratch_ptr` 与 `32768`。
- 对照腿（放行）：同一数据段挪到 1024 ⇒ 加载成功。
- 对照腿（另一侧被拒）：`__host_params_ptr=4096` 且数据段起点 4096 ⇒ 同样被拒
  （4096 不被下界拦住，只有新判据能拦）。

### 4.2 解析器腿

| 用例 | 期望 |
|---|---|
| 两个活动段（1024/256、90000/16） | 区间逐字对上，`Unprovable` 为空 |
| 无 Data section | 空图、无不可证明项（「没有」≠「读不懂」） |
| `mode=2` + memory 0 | 正常解出 |
| `mode=1` 被动段 | `Unprovable` 含 `passive` |
| `global.get` offset | `Unprovable` 含 `global.get` |
| 负偏移 / 多指令 offset | `Unprovable`（分别点名 negative / single i32.const） |
| 坏 magic / 短 header / 段长越界 / 截断 payload | `errStaticDataMalformed` |

### 4.3 不退化

- QT-01 全部用例（`pkg/sandbox/wasm/abi_check_test.go`、`pkg/backtest/contracts/abi_test.go`）保持绿。
- `TestNewOnBarSession_V1KeepsLegacyLayoutDespiteData`：v1 artifact 即使把数据放在 32768
  也照常加载并给 legacy 诊断 —— 新门没有把兼容期打死。

### 4.4 破坏验证（做过的腿）

| 破坏 | 结果 |
|---|---|
| 把 `decl.CheckAgainstStaticData(...)` 短路（`&& false`） | 立项腿 + 不可证明传播腿**两红**，报文点明「必须被拒 / 必须 fail-closed」 |
| 还原 | 两包全绿（`go test ./pkg/sandbox/wasm/ ./pkg/backtest/contracts/ -count=1`） |

---

## 5. 边界与非目标（照单）

| 不做 | 归属 |
|---|---|
| 按声明地址投递 params（`Initialize` 仍写 offset 0） | QT-26 |
| SDK 模板导出三个符号的生成方式 | QT-27 |
| 行为腿（哨兵回读）作为判据 | 本单不做，只保留为将来的补充证据 |
| 改三个符号名 / i32 类型 / 下界 / 对齐 / 新增第四个导出 | **禁止**（已冻结契约，④） |
| 改 `BarContext` | QT-24 |

---

## 6. 已知残余风险

1. **解析器覆盖面**：扩展常量表达式（`i32.add` 等后续提案）一律判为不可证明 ⇒ 该 artifact
   被拒。这是保守方向（宁可拒绝，不放行），代价是未来需要随 wasm 提案更新解析器。
2. **`DataCount` 与被动段**：本单按「被动段出现即拒绝」处理；带 bulk memory 但全是活动段的
   模块仍可正常加载。若将来 TinyGo 默认产出被动段，需要在 SDK 模板侧要求活动段布局，
   而不是放宽判定。
3. **v1 的 legacy 布局仍是「已登记的巧合」**（`DiagLegacyHostLayout`）——本单不改变这一点，
   它的收口在「新产物一律 v2」。