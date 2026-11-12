package testenv

// market_tables.go —— 测试库里的「外仓表」夹具（QT-82 裁决 a）。
//
// 背景：`SVC-CORE-DATA-RETIRE` 之后，`market` 数据面的 21 张表 owner = `tellurion-data`
// （`tellurion-data:internal/schema/market.go`），本仓的生产迁移里**不再**建它们。
// 于是只用 core 迁移搭出来的测试库缺 `ohlcv_minute` / `stock_sector_map` /
// `market_activity` / `ohlcv_daily_qfq` / `market.stocks`，而本仓仍有只读这几张表的
// 代码：`pkg/marketdata/router.go`（1m/15m/1d）、`pkg/expression` 的字段可用性探测
// （行情六字段 / `sector` / `turnover_rate`）、`pkg/storage.ScreenFundamentals`
// （`LEFT JOIN market.stocks`）。
//
// 这里按 data 仓的**列形状镜像**建空表，仅用于测试库：
//   - 不写任何生产 DDL（`pkg/storage/postgres.go` 一字未动，退役护栏照旧扫描它）；
//   - 不写数据面**写**路径（建表 ≠ 恢复写入；core 仍只有 SELECT）；
//   - 形状漂移由 data 仓的 `internal/schema/market.go` 单方定义，本文件只是镜像，
//     改那边要同步改 testdata/market_fixtures.sql（两处都写了来源，便于 diff）。
//
// 只用裸表名：与 `pkg/marketdata/router.go` 的查询口径一致（生产部署由 DSN 的
// search_path 指向 `market` schema）。
//
// DDL 放在 testdata/*.sql 而非 Go 字符串字面量，是为了不触发两条「DDL 唯一落点」
// 静态护栏（`pkg/storage:TestModuleDDLConsolidatedIntoMigrate`、
// `internal/repoguard:TestNoNewDirectPhysicalTableAccess`）；testdata 是它们
// 明确跳过的目录，且本包只被 `_test.go` 与 CI 迁移步（tools/ci/applymigrations）
// import，不构成第二份可执行真相。

import (
	"context"
	_ "embed"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// marketFixturesSQL 是外仓表的测试夹具 DDL（幂等）。
//
// 来源：`tellurion-data:internal/schema/market.go` 的 `market.ohlcv_minute`
// （第 45 行）、`market.stock_sector_map`（第 128 行）、`market.market_activity`
// （第 284 行）、`market.ohlcv_daily_qfq` 与 `market.stocks`，按 core 侧的
// 查询口径决定是否带 `market.` 前缀（裸表名 → search_path；显式限定 → schema）。
//
//go:embed testdata/market_fixtures.sql
var marketFixturesSQL string

// EnsureMarketTableFixtures 在测试库补齐外仓表镜像（幂等，不灌数据）。
//
// 调用点仅限测试；DDL 失败即 fail-loud（测试库被显式指定时，环境问题不该被吞）。
func EnsureMarketTableFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if err := EnsureMarketTableFixturesSQL(t.Context(), pool); err != nil {
		t.Fatalf("[testdb] 建外仓表夹具失败（owner = tellurion-data，形状镜像其 internal/schema/market.go）: %v", err)
	}
}

// EnsureMarketTableFixturesSQL 是不依赖 *testing.T 的同一份夹具入口。
//
// 给 CI 的迁移步（tools/ci/applymigrations）用：让各测试包并行启动时**不必**
// 各自跑一遍 CREATE TABLE IF NOT EXISTS —— 并发 DDL 在 Postgres 里有
// pg_type 唯一键竞争，偶发会把绿变成红。
func EnsureMarketTableFixturesSQL(ctx context.Context, pool *pgxpool.Pool) error {
	for _, stmt := range strings.Split(marketFixturesSQL, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
