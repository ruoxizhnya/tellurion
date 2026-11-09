// Package domain 是 core 的领域形状聚合点。
//
// 历史：QT-05 ③-①（2026-10-22）把这一整包迁去契约仓；CT-06-B（2026-11-10）
// 按补裁 #12 把「只有 core 一个使用方」的 52 个根名迁回本仓，契约仓只留
// ≥2 使用方的形状（C4）。因此本包现在有两类符号：
//
//   - 本仓实类型：`types.go` / `backtest.go` / `factor*.go` / `screen.go` /
//     `supply_chain.go` / `fundamentals_detail.go`，逐字搬自契约仓，字段与
//     JSON tag 不变；
//   - 契约仓形状的别名：见 aliases.go。`domain.X` 与契约仓同名类型是**同一个
//     类型**（type alias），只为让 core 的 286 个调用点保持单一 import。
//
// 纪律：本包不放 I/O、不放时钟、不做业务裁决；新增 core 独有形状前先看
// 契约仓是否已有一个跨仓使用方——若只有本仓用，就该住在这里，不该再进契约。
package domain
