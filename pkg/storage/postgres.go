package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-kit/logging"
)

// PostgresStore handles PostgreSQL/TimescaleDB operations.
type PostgresStore struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

// NewPostgresStore creates a new PostgresStore with the given connection string
// and **applies the inline migrations** (migrationStatements()) plus the
// builtin-factor hypothesis seed.
//
// DATA-09 (2026-10-16): this is the only carrier of「应用迁移」, and it is meant
// for the startup / deploy path — `cmd/data`, `pkg/bootstrap`, `cmd/dualrun`, and
// the `integration`-tagged suite that boots a throwaway container. It is not for
// tests: calling it from a default-build test really does rewrite the shared
// development database (measured 2026-10-15 — one `go test ./pkg/data/...`
// applied Migration 054's whole-table ×1000 to 3,926,510 rows, unauthorised and
// unannounced; the sentinel in `data_migrations` then makes it non-replayable).
// Tests connect with NewPostgresStoreConnected. The split is pinned by
// internal/repoguard/store_migration_isolation_test.go, in both directions:
// a default-build test calling this, or production calling the non-migrating
// entry, both go red.
func NewPostgresStore(ctx context.Context, connString string) (*PostgresStore, error) {
	store, err := connectPostgresStore(ctx, connString)
	if err != nil {
		return nil, err
	}

	if err := store.migrate(ctx); err != nil {
		store.pool.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	// P2-3：把内置因子的假设来源写进库（只补没有的，不覆盖人写的）。
	// 失败只记日志 —— 假设来源查不到会降级到代码里的内置表，不至于让服务起不来。
	if _, err := store.SeedBuiltinFactorHypotheses(ctx); err != nil {
		store.logger.Warn().Err(err).Msg("Failed to seed factor hypotheses — 查询会回退到代码里的内置表")
	}

	store.logger.Info().Msg("PostgreSQL/TimescaleDB connection established")
	return store, nil
}

// NewPostgresStoreConnected opens the pool and verifies it with a ping, but
// runs **no migrations and no seed** — the database is taken as it is.
//
// Seeding is excluded for the same reason migrating is: it INSERTs rows into
// factor_hypothesis, and a test run has no business writing to the shared
// development database. No test reads that table today (实测：`factor_hypothesis`
// 在 *_test.go 里零命中)；将来有用例需要它，应当自灌自清理，而不是把 seed 请回测试路径。
//
// This is the entry for DB-touching tests: the shared development database must
// keep its migration state (and its rows) outside of `go test`. It presupposes
// an already-migrated database — on a fresh one these tests fail with
// "relation … does not exist", and the fix is to start a service once (or run
// the integration-tagged suite), not to reach for NewPostgresStore.
//
// It must never be called from production code: doing so would let a service
// boot against an unmigrated schema, i.e. use this entry to bypass the
// migration gate. internal/repoguard/store_migration_isolation_test.go asserts
// zero production callers.
func NewPostgresStoreConnected(ctx context.Context, connString string) (*PostgresStore, error) {
	return connectPostgresStore(ctx, connString)
}

// connectPostgresStore builds and verifies the pool. Neither migrating nor
// seeding happens here — that is what distinguishes the two public entries.
func connectPostgresStore(ctx context.Context, connString string) (*PostgresStore, error) {
	logger := logging.WithContext(map[string]any{"component": "postgres_store"})

	poolConfig, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	poolConfig.MaxConns = PostgresMaxConns
	poolConfig.MinConns = PostgresMinConns
	poolConfig.MaxConnLifetime = PostgresConnMaxLifetime
	poolConfig.MaxConnIdleTime = PostgresConnMaxIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &PostgresStore{pool: pool, logger: logger}, nil
}

// Close closes the database connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
	s.logger.Info().Msg("PostgreSQL connection pool closed")
}

// DB returns the underlying pgxpool.Pool for direct queries.
func (s *PostgresStore) DB() *pgxpool.Pool {
	return s.pool
}

// Ping checks the database connection.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// TableHasRows 报告某张表当前是否有数据（只读可用性探测）。
//
// 用途：表达式字段目录要回答「已登记但源表为空」与「字段不存在」是两件事
// （OBS-08 切片 2 的可用性层）。表名按标识符转义后拼进 SQL
// （pgx.Identifier.Sanitize），调用方仍只应传注册表常量，不透传用户输入。
//
// 池未装配 / 查询失败时返回 error（fail-closed）：把「探测失败」当成
// 「表是空的」会让字段不可用与探测故障混为一谈。
func (s *PostgresStore) TableHasRows(ctx context.Context, table string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, fmt.Errorf("storage: TableHasRows(%q): connection pool is not configured", table)
	}
	var has bool
	query := "SELECT EXISTS (SELECT 1 FROM " + pgx.Identifier{table}.Sanitize() + " LIMIT 1)"
	if err := s.pool.QueryRow(ctx, query).Scan(&has); err != nil {
		return false, fmt.Errorf("storage: TableHasRows(%q): %w", table, err)
	}
	return has, nil
}

// migrationStatements 返回 migrate() 按序执行的全部建表 DDL：基础数组 +
// Migration 048/049 追加的包级变量（ExternalSignalsSchemaDDL /
// SupplyChainSchemaDDL）。
//
// ─── 这里是 DDL 的唯一真相（P1-4）─────────────────────────────────────
//
// 每次启动按顺序跑一遍这个数组，每条都必须是幂等的（CREATE ... IF NOT
// EXISTS / ALTER ... ADD COLUMN IF NOT EXISTS / DO 块守卫）。没有版本表、
// 没有 down、没有执行记录 —— 这是有意的取舍，理由见下。
//
// 目录里那些 .sql 的历史：
//   - `migrations/`：3 个 golang-migrate 格式的子目录 + 10 个裸编号 .sql
//   - `docs/migrations/`：另一批，含 025-027 的实际变更记录
//
// 它们**不被执行**。golang-migrate 的封装（MigrationManager）从来没有被
// 调用过，2026-09-18 删除。为什么没有改用标准迁移工具：
//   - 现有库都是这套 Go DDL 建的，没有 schema_migrations 表，接入要先
//     baseline，而 baseline 一旦和真实结构对不上，后面每条迁移都在
//     错误的假设上跑；
//   - 回测结果依赖表结构。为了工程上的整洁去动一个能用的库，收益是
//     洁癖，风险是数据。不划算。
//
// 因此约定：**加表 / 加列就在这个数组末尾追加一条，并写上
// `// Migration 0NN:` 注释**（编号续最大的那个）。散落的 .sql 留作历史
// 记录，只读、不维护、不进导航。
//
// 抽成独立函数（数组仍是字面量、零行为变化）是为了让一致性测试能在**无库**
// 环境下比对「migrate() 实际执行的语句 ≡ 包级 DDL 变量」：改基础数组尾部的
// 追加内容、或改包级变量任一处而不同步另一处 → 测试红。防的是两条建表路径
// 漂移——而两处都自称正确时，谁都发现不了（K8 破坏腿③同款 hazard）。
func migrationStatements() []string {
	migrations := []string{
		// SVC-CORE-DATA-RETIRE（2026-11-08）：stocks / ohlcv_daily_qfq 的建表
		// 已随数据面退役（owner = 数据服务仓）。本仓只保留非市场域的 DDL。
		`CREATE TABLE IF NOT EXISTS stock_fundamentals (
			id SERIAL PRIMARY KEY,
			ts_code VARCHAR(20) NOT NULL,
			trade_date DATE NOT NULL,
			ann_date DATE,
			end_date DATE,
			pe FLOAT,
			pb FLOAT,
			ps FLOAT,
			roe FLOAT,
			roa FLOAT,
			debt_to_equity FLOAT,
			gross_margin FLOAT,
			net_margin FLOAT,
			revenue FLOAT,
			net_profit FLOAT,
			total_assets FLOAT,
			total_liab FLOAT,
			created_at TIMESTAMP DEFAULT NOW(),
			UNIQUE(ts_code, trade_date)
		)`,
		// SVC-CORE-DATA-RETIRE（2026-11-08）：trading_calendar / dividends /
		// index_constituents 的建表已随数据面退役（owner = 数据服务仓）。
		// Migration 006: docs/migrations/006_add_factor_cache_table.sql
		`CREATE TABLE IF NOT EXISTS factor_cache (
			id SERIAL PRIMARY KEY,
			symbol VARCHAR(20) NOT NULL,
			trade_date DATE NOT NULL,
			factor_name VARCHAR(20) NOT NULL,
			raw_value DOUBLE PRECISION,
			z_score DOUBLE PRECISION,
			percentile DOUBLE PRECISION,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// SVC-CORE-DATA-RETIRE（2026-11-08）：splits 的建表已随数据面退役。
		// Migration 024: docs/migrations/024_add_factor_returns_table.sql
		`CREATE TABLE IF NOT EXISTS factor_returns (
			id SERIAL PRIMARY KEY,
			factor_name VARCHAR(20) NOT NULL,
			trade_date DATE NOT NULL,
			quintile INTEGER NOT NULL CHECK (quintile BETWEEN 1 AND 5),
			avg_return DOUBLE PRECISION,
			cumulative_return DOUBLE PRECISION,
			top_minus_bot DOUBLE PRECISION,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// Migration 010: ic_analysis table
		`CREATE TABLE IF NOT EXISTS ic_analysis (
			id SERIAL PRIMARY KEY,
			factor_name VARCHAR(20) NOT NULL,
			trade_date DATE NOT NULL,
			ic DOUBLE PRECISION,
			p_value DOUBLE PRECISION,
			top_ic DOUBLE PRECISION,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// SVC-CORE-RUN-RETIRE（2026-11-09）：walk_forward_reports 的建表已退役
		// （owner = orchestration 的 run.walk_forward_reports）。core 只保留
		// WF 计算（POST /api/walkforward 直接返回报告，不落库）。
		// SVC-CORE-STRAT-RETIRE（2026-11-09）：Migration 011 的 strategies 表已退役
		// （定义真相源 = tellurion-strategy 的 strategy.strategy_definitions；core
		// 只保留运行时 registry）。见 docs/SPEC.md §Strategy Management。
		// Migration 009: backtest_jobs table (async job queue)
		`CREATE TABLE IF NOT EXISTS backtest_jobs (
			id VARCHAR(64) PRIMARY KEY,
			strategy_id VARCHAR(50) NOT NULL,
			params JSONB NOT NULL DEFAULT '{}',
			universe VARCHAR(100) NOT NULL,
			start_date DATE NOT NULL,
			end_date DATE NOT NULL,
			status VARCHAR(20) NOT NULL DEFAULT 'pending',
			result JSONB,
			error_message TEXT,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			started_at TIMESTAMPTZ,
			completed_at TIMESTAMPTZ
		)`,
		// REV-18（2026-10-17）：qfq 复权基准快照。一个 symbol 在某个回测区间
		// 起点首次被取到时，把该区间首根 bar 的 (anchor_date, anchor_close) 冻结
		// 在这里；之后同区间重跑把当前序列缩放回冻结刻度，保证「同区间重跑
		// 逐位一致」不随数据同步推进而漂移（tushare.go 的前复权口径注释）。
		// PK (symbol, start_date) + 首写者胜（Save 用 ON CONFLICT DO NOTHING）：
		// 基准一旦冻结就不可被后来的运行改写。
		`CREATE TABLE IF NOT EXISTS backtest_adjustment_anchors (
			symbol VARCHAR(20) NOT NULL,
			start_date DATE NOT NULL,
			anchor_date DATE NOT NULL,
			anchor_close DOUBLE PRECISION NOT NULL,
			series_digest TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (symbol, start_date)
		)`,
		// Migration 019: auth tables (ADR-017 §2 / P1-2)
		`CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			username VARCHAR(64) NOT NULL UNIQUE,
			password_hash VARCHAR(120) NOT NULL,
			role VARCHAR(16) NOT NULL DEFAULT 'viewer',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_login_at TIMESTAMPTZ,
			disabled BOOLEAN NOT NULL DEFAULT FALSE
		)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			role VARCHAR(16),
			ip INET,
			endpoint TEXT NOT NULL,
			method VARCHAR(8) NOT NULL,
			payload_hash VARCHAR(64),
			trace_id VARCHAR(64),
			status_code INT NOT NULL,
			timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		// Migration 021: docs/migrations/021_add_research_schema.sql (ADR-022 §2 类 E)
		`CREATE SCHEMA IF NOT EXISTS research`,
		`CREATE TABLE IF NOT EXISTS research.profile (
			ticker VARCHAR(20) PRIMARY KEY,
			name VARCHAR(100) NOT NULL,
			schema_version INT NOT NULL DEFAULT 1,
			source_file TEXT,
			source_mtime TIMESTAMPTZ,
			last_researched DATE,
			needs_review BOOLEAN NOT NULL DEFAULT FALSE,
			review_reason TEXT,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS research.conclusion (
			ticker VARCHAR(20) NOT NULL REFERENCES research.profile(ticker) ON DELETE CASCADE,
			conclusion_id VARCHAR(16) NOT NULL,
			title TEXT NOT NULL,
			body TEXT,
			as_of VARCHAR(16),
			confidence VARCHAR(8),
			status VARCHAR(16) NOT NULL DEFAULT 'active',
			citations JSONB NOT NULL DEFAULT '[]',
			evidence_pointer TEXT,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (ticker, conclusion_id)
		)`,
		`CREATE TABLE IF NOT EXISTS research.question (
			ticker VARCHAR(20) NOT NULL REFERENCES research.profile(ticker) ON DELETE CASCADE,
			question_id VARCHAR(16) NOT NULL,
			text TEXT NOT NULL,
			status VARCHAR(16) NOT NULL DEFAULT 'open',
			raised_at VARCHAR(16),
			PRIMARY KEY (ticker, question_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_research_conclusion_ticker ON research.conclusion(ticker)`,
		`CREATE INDEX IF NOT EXISTS idx_research_conclusion_status ON research.conclusion(status)`,
		`CREATE INDEX IF NOT EXISTS idx_research_question_ticker ON research.question(ticker)`,
		`CREATE INDEX IF NOT EXISTS idx_research_question_status ON research.question(status)`,
		`CREATE INDEX IF NOT EXISTS idx_research_profile_needs_review ON research.profile(needs_review) WHERE needs_review = TRUE`,
		// Migration 022: docs/migrations/022_equitydeep_fundamentals.sql (契约 C1, EQD-P1-1)
		// 逐字段行存 + ann_date PIT 对齐 + snapshot_uri 溯源。仅新增表，不改存量表。
		`CREATE TABLE IF NOT EXISTS fundamentals_detail (
			ts_code        VARCHAR(12)  NOT NULL,
			end_date       DATE         NOT NULL,
			ann_date       DATE         NOT NULL,
			field_code     VARCHAR(64)  NOT NULL,
			raw_field_name VARCHAR(128) NOT NULL,
			value          NUMERIC(24,4),
			unit           VARCHAR(16)  NOT NULL,
			source         VARCHAR(32)  NOT NULL,
			fetched_at     TIMESTAMPTZ  NOT NULL,
			snapshot_uri   TEXT         NOT NULL,
			PRIMARY KEY (ts_code, end_date, ann_date, field_code, fetched_at)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_fund_detail_lookup
			ON fundamentals_detail (ts_code, field_code, end_date DESC)`,
		// Migration 028: experiments (P1-1 / S1 通回路)
		// AI 每次尝试落一行，回答五个问题：从哪开始(hypothesis) · 试了什么(params) ·
		// 结果怎样(metrics) · 怎么走到这(seq + parent_id) · 用的哪份数据(dataset_split)。
		// 失败也必须留行 —— 「试几次才撞出来」本身就是过拟合检测的证据
		// （PRODUCT §验证器：试 5 次和试 500 次撞出来的，可信度差一个量级）。
		// 因此 status 停在 running 的行不是垃圾数据，那是「跑到第几步被叫停」，
		// 清理它就等于抹掉回路中断的位置。
		// UNIQUE(run_id, seq)：一次 run 内第 n 次尝试只有一条，路径回放才不会歧义。
		`CREATE TABLE IF NOT EXISTS experiments (
			id            BIGSERIAL PRIMARY KEY,
			run_id        VARCHAR(64)  NOT NULL,
			seq           INT          NOT NULL,
			parent_id     BIGINT       REFERENCES experiments(id) ON DELETE SET NULL,
			hypothesis    TEXT,
			params        JSONB        NOT NULL DEFAULT '{}',
			dataset_split VARCHAR(16)  NOT NULL DEFAULT 'train',
			strategy_name VARCHAR(100),
			expression    TEXT,
			status        VARCHAR(16)  NOT NULL DEFAULT 'running',
			metrics       JSONB,
			verdict       JSONB,
			error_message TEXT,
			created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
			finished_at   TIMESTAMPTZ,
			UNIQUE (run_id, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_experiments_run_seq ON experiments(run_id, seq)`,
		// Migration 029: experiments.verdict (P2-9 接线 / A)
		// 验证器链每次尝试后跑一遍，裁决（概率 + 质疑清单）写回那一行。
		// 为什么要落库而不是只在内存里传给前端：runs 是进程内 map，一重启裁决
		// 就没了，而「这次尝试当时被质疑了什么」是复盘时唯一能回答
		// 「为什么当初没采纳它」的东西 —— 它和 metrics 一样是实验的证据。
		// 加列带默认值即 metadata-only，幂等。
		`ALTER TABLE experiments ADD COLUMN IF NOT EXISTS verdict JSONB`,
		// SVC-CORE-DATA-RETIRE（2026-11-08）：Migration 030（stocks.delist_date）
		// 随 stocks 表退役；幸存者偏差的上市/摘牌日改由数据服务仓提供。
		// Migration 031: stock_fundamentals.available_date (P1-4 / 语义债)
		// 「这条财报从哪天起可见」此前没有自己的列，只在 5 个查询里以
		// COALESCE(ann_date, trade_date) 的形式散着写 —— 另外 5 个查询压根
		// 没写，直接拿 trade_date 当可见日，而 trade_date 装的是报告期截止日。
		// 语义散在调用方脑子里，就一定会有人忘。
		//
		// 现在钉成一个列：available_date = COALESCE(ann_date, trade_date)。
		// ann_date 为 NULL 时它仍等于报告期截止日（那笔债还在，见路径 A），
		// 但**列的存在让债可见** —— 可以数得出来有多少行 ann_date 是空的，
		// 也可以在回填之后把缺口补上。
		// 用**生成列**而不是普通列：普通列要靠每个写入函数记得填，而
		// 任何绕过写入函数的路径（手工 INSERT、修数、别的 ETL）都会留下
		// 一行 available_date 为 NULL 的数据 —— 它随后会从所有按可用日
		// 过滤的查询里**凭空消失**，而且看不出为什么少了一只票。
		// 生成列从物理上保证：它不可能为空，也不可能和 COALESCE 的结果
		// 不一致，加列时存量自动算好（不需要回填 UPDATE）。
		//
		// 代价：不能 INSERT/UPDATE 这一列，写入函数里必须去掉它。
		`ALTER TABLE stock_fundamentals
			ADD COLUMN IF NOT EXISTS available_date DATE
			GENERATED ALWAYS AS (COALESCE(ann_date, trade_date)) STORED`,
		// 按可用日过滤是这个表最主要的读法，给个索引。
		`CREATE INDEX IF NOT EXISTS idx_stock_fundamentals_available
			ON stock_fundamentals (ts_code, available_date DESC)`,
		// 清理历史脏数据：domain.FundamentalData.AnnDate 是 time.Time 而非
		// 指针，缺字段时写进去的是零值 0001-01-01 —— 那不是披露日，是"没填"。
		// 它让 COALESCE(ann_date, trade_date) 拿到一个公元前的值，整行数据
		// 从此在所有按可用日过滤的查询里消失。存量的这类行必须清成 NULL，
		// 否则加完列反而会少数据（写入侧已用 nullableDate 堵住新增）。
		`UPDATE stock_fundamentals SET ann_date = NULL WHERE ann_date < DATE '1900-01-01'`,
		// ── DML 迁移的一次性标记表 ──
		//
		// migrationStatements() 里绝大多数是幂等 DDL（IF NOT EXISTS），每次启动
		// 重跑等价 no-op。但**变换型 DML**（如 052 的整表 ×100）不幂等，且
		// 「是否已执行」无法从数据本身可靠推断（见 052 的注释）。故给这类迁移
		// 一个显式的一次性记录。后续同类迁移**复用本表**，不要另立机制。
		`CREATE TABLE IF NOT EXISTS data_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		// SVC-CORE-DATA-RETIRE（2026-11-08）：Migration 052 / 054（OHLCV 成交量
		// 与成交额的整表单位换算）随 ohlcv_daily_qfq 退役——目标表已由数据服务仓
		// owner，core 不再对市场表做变换型 DML。data_migrations 表保留，供后续
		// 仍属本仓的一次性迁移复用（当前无使用者）。
		// Migration 032: factor_hypothesis (P2-3 / 因子的因果来源)
		//
		// 因子的数值存在 factor_cache 里，但**它为什么该有效**此前没有地方记。
		// 缺了这一条就没法区分「有经济学依据」和「数据挖掘挖出来的」—— 而
		// 后者正是过拟合的主要来源：在数据上试出来的相关性，换个时间段就散。
		//
		// 为什么是独立的表而不是 factor_cache 的一列：假设是**因子级**元数据，
		// 而 factor_cache 是 symbol × date × factor 的行级缓存 —— 存成列会让
		// 同一个字符串重复几十万次，改一次要全表 UPDATE。
		`CREATE TABLE IF NOT EXISTS factor_hypothesis (
			factor_name  VARCHAR(32) PRIMARY KEY,
			source_kind  VARCHAR(24) NOT NULL,
			hypothesis   TEXT        NOT NULL,
			reference    TEXT,
			created_at   TIMESTAMPTZ DEFAULT NOW(),
			updated_at   TIMESTAMPTZ DEFAULT NOW()
		)`,
		// 把「已经是普通列」的存量安装升级成生成列。
		//
		// ADD COLUMN IF NOT EXISTS 只管"列在不在"，不管"列是怎么算的"：
		// 早期那版把它建成普通 DATE 列，之后再跑到的 GENERATED 版就会因为
		// IF NOT EXISTS 被整个跳过 —— 结果是一个永远为 NULL 的普通列，
		// 所有按可用日过滤的查询静默返回空。这个 DO 块按
		// generation_expression 是否为空来判断，只在实际需要时重建一次，
		// 之后重复启动等价 no-op。
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'stock_fundamentals'
				  AND column_name = 'available_date'
				  AND coalesce(generation_expression, '') = ''
			) THEN
				ALTER TABLE stock_fundamentals DROP COLUMN available_date;
				ALTER TABLE stock_fundamentals
					ADD COLUMN available_date DATE
					GENERATED ALWAYS AS (COALESCE(ann_date, trade_date)) STORED;
			END IF;
		END $$`,
		// Migration 026: docs/migrations/026_widen_factor_name.sql (EQD-P1-2 / 桥 B1)
		// 桥 B1 的 5 个纵向基本面因子名最长 24 字符，超出既有 VARCHAR(20)。
		// 因子链路的三个表同源同一列，须同时放宽；加宽 varchar 为 metadata-only，不改写表。
		`ALTER TABLE factor_cache ALTER COLUMN factor_name TYPE VARCHAR(32)`,
		`ALTER TABLE factor_returns ALTER COLUMN factor_name TYPE VARCHAR(32)`,
		`ALTER TABLE ic_analysis ALTER COLUMN factor_name TYPE VARCHAR(32)`,
		// Migration 027: docs/migrations/027_factor_cache_citation.sql (ODR-061 / P5-1 切片 C2)
		// factor_cache 因子行携带来源批次坐标 [{"content_hash":"<64hex>"}]，输出面展开为
		// ADR-022 §5 五元组。'[]' = 该数字的 A→B 链尚未建立（momentum/value/quality 及
		// 历史行），与 NULL 相对。带默认值加列为 metadata-only；幂等，重复启动等价 no-op。
		`ALTER TABLE factor_cache ADD COLUMN IF NOT EXISTS citation JSONB NOT NULL DEFAULT '[]'::jsonb`,
		// Migration 025: docs/migrations/025_equitydeep_field_consolidation.sql (EQD-P3-1 / C-8)
		// fundamentals 与 stock_fundamentals 的 12 个指标列同名同义，两条写入路径同源于
		// tushare fina_indicator 且都把 trade_date 取成 end_date，故存量按 symbol→ts_code
		// 直通并入幸存表 stock_fundamentals 后 DROP 旧表。DO 块守卫存在性以保证重复启动为 no-op;
		// 冲突时保留幸存表已有非空值（COALESCE 方向为「幸存表优先」）。
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = 'fundamentals'
			) THEN
				INSERT INTO stock_fundamentals (
					ts_code, trade_date, end_date,
					pe, pb, ps, roe, roa, debt_to_equity, gross_margin, net_margin,
					revenue, net_profit, total_assets, total_liab
				)
				SELECT f.symbol, f.trade_date, f.trade_date,
					f.pe, f.pb, f.ps, f.roe, f.roa, f.debt_to_equity, f.gross_margin,
					f.net_margin, f.revenue, f.net_profit, f.total_assets, f.total_liab
				FROM fundamentals f
				ON CONFLICT (ts_code, trade_date) DO UPDATE SET
					pe             = COALESCE(stock_fundamentals.pe, EXCLUDED.pe),
					pb             = COALESCE(stock_fundamentals.pb, EXCLUDED.pb),
					ps             = COALESCE(stock_fundamentals.ps, EXCLUDED.ps),
					roe            = COALESCE(stock_fundamentals.roe, EXCLUDED.roe),
					roa            = COALESCE(stock_fundamentals.roa, EXCLUDED.roa),
					debt_to_equity = COALESCE(stock_fundamentals.debt_to_equity, EXCLUDED.debt_to_equity),
					gross_margin   = COALESCE(stock_fundamentals.gross_margin, EXCLUDED.gross_margin),
					net_margin     = COALESCE(stock_fundamentals.net_margin, EXCLUDED.net_margin),
					revenue        = COALESCE(stock_fundamentals.revenue, EXCLUDED.revenue),
					net_profit     = COALESCE(stock_fundamentals.net_profit, EXCLUDED.net_profit),
					total_assets   = COALESCE(stock_fundamentals.total_assets, EXCLUDED.total_assets),
					total_liab     = COALESCE(stock_fundamentals.total_liab, EXCLUDED.total_liab);
				DROP TABLE fundamentals;
			END IF;
		END $$`,

		// SVC-CORE-DATA-RETIRE（2026-11-08）：Migration 033-043 的 11 张多源
		// 数据面目标表（realtime_quote / ohlcv_minute / capital_flow / sectors /
		// stock_sector_map / top_list / limit_up_pool / announcements / news /
		// hot_search / global_ohlcv）以及 Migration 044/045 的 sync_jobs /
		// sync_schedules，已随数据面整体退役——owner = 数据服务仓，core 不再
		// 持有这些表的 DDL、写点或同步队列。
		// Migration 046: factor_genes (AI 因子基因池)
		`CREATE TABLE IF NOT EXISTS factor_genes (
			id          VARCHAR(50) PRIMARY KEY,
			name        VARCHAR(100) NOT NULL,
			category    VARCHAR(30) NOT NULL,
			formula     TEXT NOT NULL,
			description TEXT,
			rationale   TEXT,
			ic          DOUBLE PRECISION DEFAULT 0,
			ir          DOUBLE PRECISION DEFAULT 0,
			turnover    DOUBLE PRECISION DEFAULT 0,
			sharpe      DOUBLE PRECISION DEFAULT 0,
			fitness     DOUBLE PRECISION DEFAULT 0,
			generation  INTEGER DEFAULT 0,
			parent_ids  JSONB DEFAULT '[]',
			status      VARCHAR(20) DEFAULT 'pending',
			created_at  TIMESTAMPTZ DEFAULT NOW(),
			updated_at  TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_factor_genes_category ON factor_genes(category)`,
		`CREATE INDEX IF NOT EXISTS idx_factor_genes_status ON factor_genes(status)`,
		`CREATE INDEX IF NOT EXISTS idx_factor_genes_fitness ON factor_genes(fitness DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_factor_genes_cat_status ON factor_genes(category, status)`,

		// Migration 047: strategy_genes (AI 策略基因池)
		`CREATE TABLE IF NOT EXISTS strategy_genes (
			id            VARCHAR(50) PRIMARY KEY,
			name          VARCHAR(100) NOT NULL,
			description   TEXT,
			strategy_type VARCHAR(30) NOT NULL,
			code          TEXT,
			params        JSONB DEFAULT '{}',
			factor_ids    JSONB DEFAULT '[]',
			parent_ids    JSONB DEFAULT '[]',
			total_return  DOUBLE PRECISION DEFAULT 0,
			sharpe        DOUBLE PRECISION DEFAULT 0,
			max_drawdown  DOUBLE PRECISION DEFAULT 0,
			win_rate      DOUBLE PRECISION DEFAULT 0,
			fitness       DOUBLE PRECISION DEFAULT 0,
			generation    INTEGER DEFAULT 0,
			status        VARCHAR(20) DEFAULT 'pending',
			created_at    TIMESTAMPTZ DEFAULT NOW(),
			updated_at    TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_strategy_genes_type ON strategy_genes(strategy_type)`,
		`CREATE INDEX IF NOT EXISTS idx_strategy_genes_status ON strategy_genes(status)`,
		`CREATE INDEX IF NOT EXISTS idx_strategy_genes_fitness ON strategy_genes(fitness DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_strategy_genes_type_status ON strategy_genes(strategy_type, status)`,

		// Migration 051: factor_definitions (D3 公式化因子引擎——定义入库)
		// 「库里公式」的库：定义（名字/公式/输入字段/版本哈希/生效日/口径/
		// 前视纪律）的持久形态。version_hash 由定义内容派生
		// (domain.FactorDefinition.VersionHash)，加载时重新 derive 校验——
		// 库里存的哈希与内容不符即拒绝加载（fail-closed）。
		`CREATE TABLE IF NOT EXISTS factor_definitions (
			factor_name    VARCHAR(64) PRIMARY KEY,
			kernel         VARCHAR(16) NOT NULL,
			formula        TEXT NOT NULL DEFAULT '',
			validity       TEXT NOT NULL DEFAULT '',
			code_ref       VARCHAR(128) NOT NULL DEFAULT '',
			caliber        VARCHAR(16) NOT NULL,
			input_fields   TEXT[] NOT NULL DEFAULT '{}',
			effective_from VARCHAR(10) NOT NULL,
			lookahead      VARCHAR(64) NOT NULL,
			lookback       INT NOT NULL DEFAULT 0,
			version_hash   CHAR(64) NOT NULL,
			updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	}

	// Migration 048: docs/migrations/048_add_external_signals_table.sql
	// quant.external_signals（K8 / K7 遗留①：信号表并入 migrate 唯一执行路径）。
	// DDL 文本在包级 ExternalSignalsSchemaDDL——与 pkg/strategy.SignalSchemaDDL
	// 归一化空白后逐条相等，由 pkg/strategy 的一致性测试钉住（破坏腿③：
	// 两侧改一处不改另一处 → 测试红）。
	migrations = append(migrations, ExternalSignalsSchemaDDL...)

	// Migration 049: docs/migrations/049_add_supply_chain_tables.sql
	// 产业链静态数据层四表（P2-2，TASKS.md §五 遗留待裁决）：
	// quant.sw_industry_tree / supply_chain_edge / industry_vitality /
	// etf_constituent，统一带 source/as_of 溯源列与幂等唯一约束。
	// DDL 文本在包级 SupplyChainSchemaDDL——与 docs/migrations/049 镜像副本
	// 归一化空白后逐条相等，由 pkg/storage 的一致性测试钉住（破坏腿：
	// 改本追加内容或改 SupplyChainSchemaDDL 任一处 → 测试红）。
	migrations = append(migrations, SupplyChainSchemaDDL...)

	// Migration 050: docs/migrations/050_add_macro_series_table.sql
	// 宏观指标序列表 quant.macro_series（P2-1，TASKS.md §五 遗留待裁决）：
	// 国家层宏观观测（CPI/PPI/PMI/M2/社融/LPR/汇率），与 049 的
	// industry_vitality（行业景气）并列不重复。统一带 source/as_of 溯源列
	// 与幂等唯一约束（indicator+freq+obs_date+source）。
	// DDL 文本在包级 MacroSeriesSchemaDDL——与 docs/migrations/050 镜像副本
	// 归一化空白后逐条相等，由 pkg/storage 的一致性测试钉住（破坏腿：
	// 改本追加内容或改 MacroSeriesSchemaDDL 任一处 → 测试红）。
	migrations = append(migrations, MacroSeriesSchemaDDL...)

	// Migration 053: factor_registrations + factor_registration_audit（D4 因子
	// 注册表治理细则——治理账本）。DDL 记录在 docs/migrations/053。
	// 与 051 的 factor_definitions（最新投影）互补：本表是治理账本，一条登记
	// = 一行（factor_name + version_hash 主键），定义快照存 JSONB（回滚后的
	// 老版本自包含、可复现）。四样齐全的 DB 兜底：registrar 非空 CHECK + 内核体
	// 存在 CHECK + 状态枚举 CHECK——连「绕过写入路径直接 INSERT」也被库拒绝。
	migrations = append(migrations, []string{
		`CREATE TABLE IF NOT EXISTS factor_registrations (
			factor_name     VARCHAR(64)  NOT NULL,
			version_hash    CHAR(64)     NOT NULL,
			definition      JSONB        NOT NULL,
			registrar       TEXT         NOT NULL,
			status          VARCHAR(16)  NOT NULL DEFAULT 'active',
			registered_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
			rolled_back_at  TIMESTAMPTZ,
			rolled_back_by  TEXT         NOT NULL DEFAULT '',
			rollback_reason TEXT         NOT NULL DEFAULT '',
			PRIMARY KEY (factor_name, version_hash),
			CONSTRAINT factor_registrations_registrar_present CHECK (btrim(registrar) <> ''),
			CONSTRAINT factor_registrations_status_known CHECK (status IN ('active', 'rolled_back')),
			CONSTRAINT factor_registrations_kernel_body_present CHECK (
				COALESCE(definition ->> 'kernel', '') IN ('leaf', 'formula', 'code')
				AND (COALESCE(definition ->> 'formula', '') <> '' OR COALESCE(definition ->> 'code_ref', '') <> '')
			)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_factor_registrations_status ON factor_registrations(factor_name, status)`,
		`CREATE TABLE IF NOT EXISTS factor_registration_audit (
			id           BIGSERIAL    PRIMARY KEY,
			factor_name  VARCHAR(64)  NOT NULL,
			version_hash CHAR(64)     NOT NULL,
			action       VARCHAR(16)  NOT NULL,
			actor        TEXT         NOT NULL,
			reason       TEXT         NOT NULL DEFAULT '',
			detail       TEXT         NOT NULL DEFAULT '',
			audited_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
			CONSTRAINT factor_registration_audit_action_known CHECK (action IN ('register', 'rollback'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_factor_registration_audit_name ON factor_registration_audit(factor_name, audited_at DESC)`,
	}...)

	// Migration 055: factor_mining_records（D8 因子挖掘留痕——治理层审计）。
	// DDL 记录在 docs/migrations/055。与 053 的登记账本互补：登记账本记
	// 「谁把哪一版接进来」，本表记「这一版凭什么进库」——来源（谁生成/第几代/
	// 依据）、沙箱凭证哈希、过拟合与样本外判定。一条 = 一行（factor_name +
	// version_hash 主键）。DB 兜底 CHECK：生成器非空 + 来源枚举 + 尝试次数为正
	// + 已入库必须带绑定该版本的沙箱凭证 + 被拒必须写明原因——绕过写入路径
	// 直接 INSERT 缺样，也被库拒绝（fail-closed）。
	migrations = append(migrations, []string{
		`CREATE TABLE IF NOT EXISTS factor_mining_records (
			factor_name    VARCHAR(64)      NOT NULL,
			version_hash   CHAR(64)         NOT NULL,
			source_kind    VARCHAR(32)      NOT NULL,
			generator      TEXT             NOT NULL,
			generation     INT              NOT NULL DEFAULT 0,
			rationale      TEXT             NOT NULL DEFAULT '',
			num_trials     INT              NOT NULL,
			failed_trials  INT              NOT NULL DEFAULT 0,
			sandbox_hash   CHAR(64)         NOT NULL DEFAULT '',
			admitted       BOOLEAN          NOT NULL DEFAULT FALSE,
			reject_reason  TEXT             NOT NULL DEFAULT '',
			overfit_raw_p  DOUBLE PRECISION NOT NULL DEFAULT 0,
			overfit_adj_p  DOUBLE PRECISION NOT NULL DEFAULT 0,
			overfit_flag   BOOLEAN          NOT NULL DEFAULT FALSE,
			oos_in_sample  DOUBLE PRECISION NOT NULL DEFAULT 0,
			oos_out_sample DOUBLE PRECISION NOT NULL DEFAULT 0,
			oos_pass       BOOLEAN          NOT NULL DEFAULT FALSE,
			created_at     TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
			PRIMARY KEY (factor_name, version_hash),
			CONSTRAINT factor_mining_records_generator_present CHECK (btrim(generator) <> ''),
			CONSTRAINT factor_mining_records_source_known CHECK (
				source_kind IN ('ai_hypothesis', 'literature', 'supply_chain', 'ad_hoc')
			),
			CONSTRAINT factor_mining_records_trials_coherent CHECK (
				num_trials > 0 AND failed_trials >= 0 AND failed_trials <= num_trials
			),
			CONSTRAINT factor_mining_records_admitted_attested CHECK (
				NOT admitted OR sandbox_hash = version_hash
			),
			CONSTRAINT factor_mining_records_rejected_reason CHECK (
				admitted OR btrim(reject_reason) <> ''
			)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_factor_mining_records_name ON factor_mining_records(factor_name, created_at DESC)`,
	}...)

	// SVC-CORE-DATA-RETIRE（2026-11-08）：Migration 056（market_activity）随
	// 数据面退役——个股日频活跃度由数据服务仓 owner，core 不再建表/写入。
	//
	// Migration 057：market schema + market.index_daily（DATA-12，2026-10-16）。
	//
	// 指数日线是 **B 类规范化数据**（A–E 分区表：B = PG `market.*`）——落点是
	// 独立新表，**不是**塞进 `ohlcv_daily_qfq`：那张表全是股票，指数代码进去会被
	// 票池 / 因子计算 / NT 导出当成一只普通股票看见（工单③点名要避开的形态）。
	// 全库此前没有 `market` schema（2026-10-16 实测），这里一并建。
	//
	// 单位沿用规范层已定死的口径：volume **股**、turnover **CNY**。源侧 tushare
	// `index_daily` 的 `vol` 是**手**、`amount` 是**千元**（2026-10-16 实测校准：
	// 沪深300 成分股当日量额求和 vs index_daily 原值，×100 / ×1000 后比值 0.949/0.951），
	// 换算在同步边界（数据服务仓）完成。
	//
	// 缺失≠0：除 `close` / `volume` 外的数值列可空，NULL = 上游未发布，
	// 绝不落 0 冒充有效观测（读侧对 turnover 用指针接）。
	//
	// 幂等键 `(code, trade_date)` = 同键重导刷新行（ON CONFLICT DO UPDATE，
	// 见 pkg/storage/index_daily.go）。
	migrations = append(migrations, []string{
		`CREATE SCHEMA IF NOT EXISTS market`,
		`CREATE TABLE IF NOT EXISTS market.index_daily (
			id         BIGSERIAL       PRIMARY KEY,
			code       VARCHAR(20)     NOT NULL,
			trade_date DATE            NOT NULL,
			open       DOUBLE PRECISION,
			high       DOUBLE PRECISION,
			low        DOUBLE PRECISION,
			close      DOUBLE PRECISION NOT NULL,
			pre_close  DOUBLE PRECISION,
			change     DOUBLE PRECISION,
			pct_chg    DOUBLE PRECISION,
			volume     DOUBLE PRECISION NOT NULL,
			turnover   DOUBLE PRECISION,
			source     VARCHAR(64)     NOT NULL,
			as_of      TIMESTAMPTZ     NOT NULL,
			CONSTRAINT market_index_daily_identity_present CHECK (
				btrim(code) <> '' AND btrim(source) <> ''
			),
			UNIQUE (code, trade_date)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_market_index_daily_trade_date ON market.index_daily(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_market_index_daily_code ON market.index_daily(code)`,
	}...)

	// Migration 054（SPEC-R-01）：audit.message_log 由 pkg/eventstore 的
	// EnsureSchema 兜底建表，这里收编为 migrate() 的唯一真相。表结构零变更。
	migrations = append(migrations, AuditSchemaDDL...)
	// Migration 055（SPEC-R-01）：quant.strategy_state 由 pkg/strategy 的
	// EnsureSchema 兜底建表，这里收编为 migrate() 的唯一真相。表结构零变更。
	migrations = append(migrations, StrategyStateSchemaDDL...)
	// Migration 056（QT-63）：quant.run_provenance —— 一次 run 的输入身份。
	// 只存输入、不存状态（status 的权威是 backtest_jobs）；落点裁决与 O1 的
	// 迁移债见 docs/TASKS.md QT-63。
	migrations = append(migrations, RunProvenanceSchemaDDL...)

	// Migration 059（B6-3）：backtest_jobs.result_status —— K9 三值的持久化面。
	// 与 orchestration `run.backtest_jobs` 的 DDL 对齐（completed / invalid /
	// failed），两轴**不合并**：status 仍是队列生命周期，result_status 才是
	// 结果结论。历史 009 的建表语句不改写（迁移只允许追加）。
	migrations = append(migrations, []string{
		`ALTER TABLE backtest_jobs ADD COLUMN IF NOT EXISTS result_status VARCHAR(20)`,
		`ALTER TABLE backtest_jobs DROP CONSTRAINT IF EXISTS backtest_jobs_result_status_check`,
		`ALTER TABLE backtest_jobs ADD CONSTRAINT backtest_jobs_result_status_check
			CHECK (result_status IS NULL OR result_status IN ('completed','invalid','failed'))`,
		`CREATE INDEX IF NOT EXISTS idx_bj_result_status ON backtest_jobs(result_status)`,
		// 存量回填：迁移前完成的作业只可能是有效结果（旧代码把 invalid 写成
		// failed），补成 completed 让读面无需猜。
		`UPDATE backtest_jobs SET result_status = 'completed' WHERE status = 'completed' AND result_status IS NULL`,
	}...)

	// Migration 060（QT-42 X3c）：quant.alert_outbox —— 告警投递的发件箱。
	// risk 侧的账写的是「事件回传允许延迟、不允许丢」；进程内队列做不到那一句
	// （满则丢、重启则丢），所以告警先落一行再投，失败只是「这行还是 pending」。
	migrations = append(migrations, AlertOutboxSchemaDDL...)

	// Migration 061（QT-76 缺口①）：quant.alert_outbox 的行级领取租约。
	// 追加而不是改写 060：迁移只允许追加；老库靠 ADD COLUMN IF NOT EXISTS 补齐。
	migrations = append(migrations, AlertOutboxLeaseSchemaDDL...)

	return migrations
}

// migrate 按序执行 migrationStatements() 的每条 DDL（全部幂等），随后建
// TimescaleDB hypertable 与索引。加表 / 加列的唯一路径就是
// migrationStatements() 数组（含追加的包级变量），散落的 .sql 不执行。
func (s *PostgresStore) migrate(ctx context.Context) error {
	for _, m := range migrationStatements() {
		if _, err := s.pool.Exec(ctx, m); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	// SVC-CORE-DATA-RETIRE（2026-11-08）：ohlcv_daily_qfq 的 hypertable
	// 与市场表索引随数据面退役一并删除（owner = 数据服务仓）。

	// Create indexes
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_stock_fundamentals_code ON stock_fundamentals(ts_code)`,
		`CREATE INDEX IF NOT EXISTS idx_stock_fundamentals_date ON stock_fundamentals(trade_date)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fc_pk ON factor_cache(symbol, trade_date, factor_name)`,
		`CREATE INDEX IF NOT EXISTS idx_fc_trade_date ON factor_cache(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_fc_factor_name ON factor_cache(factor_name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fr_pk ON factor_returns(factor_name, trade_date, quintile)`,
		`CREATE INDEX IF NOT EXISTS idx_fr_trade_date ON factor_returns(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_fr_factor ON factor_returns(factor_name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ic_pk ON ic_analysis(factor_name, trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_ic_trade_date ON ic_analysis(trade_date)`,
		`CREATE INDEX IF NOT EXISTS idx_ic_factor ON ic_analysis(factor_name)`,
		// SVC-CORE-STRAT-RETIRE（2026-11-09）：idx_strategies_type / idx_strategies_active
		// 随 strategies 表一并退役。
		`CREATE INDEX IF NOT EXISTS idx_bj_status ON backtest_jobs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_bj_created_at ON backtest_jobs(created_at)`,
		// P1-2: auth indexes
		`CREATE INDEX IF NOT EXISTS idx_audit_user_time ON audit_logs(user_id, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_endpoint ON audit_logs(endpoint, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_users_role ON users(role) WHERE disabled = FALSE`,
		// Migration 021: research schema indexes (类 E 研究结构化投影)
		`CREATE INDEX IF NOT EXISTS idx_research_conclusion_ticker ON research.conclusion(ticker)`,
		`CREATE INDEX IF NOT EXISTS idx_research_conclusion_status ON research.conclusion(status)`,
		`CREATE INDEX IF NOT EXISTS idx_research_question_ticker ON research.question(ticker)`,
		`CREATE INDEX IF NOT EXISTS idx_research_question_status ON research.question(status)`,
		`CREATE INDEX IF NOT EXISTS idx_research_profile_needs_review ON research.profile(needs_review) WHERE needs_review = TRUE`,
	}

	for _, idx := range indexes {
		if _, err := s.pool.Exec(ctx, idx); err != nil {
			return fmt.Errorf("index creation failed: %w", err)
		}
	}

	s.logger.Info().Msg("Database migrations completed")
	return nil
}

// AuditSchemaDDL 是 audit schema + audit.message_log 表 + 2 个索引的建表 DDL——
// migrate() 的 Migration 054 执行的就是这组语句（SPEC-R-01）。
//
// 表结构逐字取自 contracts/kernel_modules.schema.sql（K0 冻结契约），零变更；
// pkg/eventstore.SchemaDDL 是它的别名（同一底层数组），EnsureSchema 仅作
// 「独立可安装 / 测试自证」的幂等兜底入口。
var AuditSchemaDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS audit`,
	// BusTap「先记录后分发」的落点。列清单是 K0 冻结契约，改列 = 改契约。
	//   ts        —— **业务时间**（回测=VirtualClock 当前值，实盘=墙钟），
	//                不是落库时间；Replay 按它检索。
	//   payload   —— jsonb，序列化方式见 pkg/eventstore.PGEventStore.Append 注释。
	//   publisher —— 写入方标识（store 实例持有，Message 接口里没有）。
	//   run_id    —— 一次 run 的归因锚点，可空（run 建立前的早期消息）。
	`CREATE TABLE IF NOT EXISTS audit.message_log (
		id         BIGSERIAL PRIMARY KEY,
		ts         TIMESTAMPTZ NOT NULL,
		topic      TEXT NOT NULL,
		payload    JSONB NOT NULL,
		publisher  TEXT NOT NULL,
		run_id     TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_message_log_ts ON audit.message_log (ts)`,
	`CREATE INDEX IF NOT EXISTS idx_message_log_topic_ts ON audit.message_log (topic, ts)`,
}

// StrategyStateSchemaDDL 是 quant schema + quant.strategy_state 表 + 1 个索引 +
// QT-16 身份迁移（加列 + PK 扩三元组）的 DDL——migrate() 的 Migration 055（建表）
// 与 058（旧库迁移）执行的就是这组语句（SPEC-R-01）。
//
// 表结构与 contracts/kernel_modules.schema.sql 的 strategy_state 节同步修订
// （QT-16，冻结 schema 变更评审随该工单走）；pkg/strategy.SchemaDDL 是它的别名
// （同一底层数组），EnsureSchema 仅作「独立可安装 / 测试自证」的幂等兜底入口。
var StrategyStateSchemaDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS quant`,
	// 一个 run 可跑多个策略，状态按「策略 × run × 分片」定位；同一个
	// (策略, run, shard) 只保留最新一份（upsert）。列清单是 K0 冻结契约，
	// 改列 = 改契约（QT-16 的 PK 扩三元组 + 信封五列按该工单的冻结评审走）。
	//   ts    —— **状态对应时刻**（回测 = VirtualClock 虚拟时间，
	//            实盘 = 墙钟），续跑按它跳过已处理的 bar 前缀；同一行内
	//            单调不减，回退写入被拒绝。
	//   state —— SaveState() 的原始字节，不解释内容。
	//   shard_key          —— market scope = ''；symbol scope = 标的（与
	//                         RunUniverse 同一符号口径）。symbol scope 的
	//                         N 个分片状态不再互相覆盖（N3）。
	//   信封四列            —— 恢复校验用（A9 + N3）：全部匹配才恢复，任一
	//                         不符 fail-closed 并点名。DEFAULT '' 让 QT-16
	//                         之前的旧行平滑拿到空信封——旧行不再是新格式，
	//                         恢复时被点名拒绝（不做「尽力解释旧格式」）。
	`CREATE TABLE IF NOT EXISTS quant.strategy_state (
		strategy_id          TEXT NOT NULL,
		run_id               TEXT NOT NULL,
		shard_key            TEXT NOT NULL DEFAULT '',
		ts                   TIMESTAMPTZ NOT NULL,
		state                BYTEA NOT NULL,
		state_schema_version TEXT NOT NULL DEFAULT '',
		artifact_hash        TEXT NOT NULL DEFAULT '',
		abi_version          TEXT NOT NULL DEFAULT '',
		run_identity_hash    TEXT NOT NULL DEFAULT '',
		created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (strategy_id, run_id, shard_key)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_strategy_state_run ON quant.strategy_state (run_id)`,

	// ── Migration 058（QT-16）：旧库迁移。新库由上面的 CREATE TABLE 直接
	// 建成新形状，以下语句在新库上全部幂等 no-op。 ──
	// 加列（旧行 shard_key='' 落 market 分片；信封列落空串哨兵）。
	`ALTER TABLE quant.strategy_state ADD COLUMN IF NOT EXISTS shard_key TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE quant.strategy_state ADD COLUMN IF NOT EXISTS state_schema_version TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE quant.strategy_state ADD COLUMN IF NOT EXISTS artifact_hash TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE quant.strategy_state ADD COLUMN IF NOT EXISTS abi_version TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE quant.strategy_state ADD COLUMN IF NOT EXISTS run_identity_hash TEXT NOT NULL DEFAULT ''`,
	// PK 扩三元组：先核对现存 PK 的实际列集（同名的旧 PK 是
	// (strategy_id, run_id)，只按约束名判断会把「列集没变」误判成「已迁移」），
	// 不符才 DROP + ADD——ADD CONSTRAINT 无 IF NOT EXISTS，靠这层判断幂等。
	`DO $$
	DECLARE
		pk_cols TEXT;
	BEGIN
		SELECT string_agg(a.attname, ',' ORDER BY a.attnum) INTO pk_cols
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = 'quant.strategy_state'::regclass AND i.indisprimary;
		IF pk_cols IS DISTINCT FROM 'strategy_id,run_id,shard_key' THEN
			ALTER TABLE quant.strategy_state DROP CONSTRAINT IF EXISTS strategy_state_pkey;
			ALTER TABLE quant.strategy_state ADD CONSTRAINT strategy_state_pkey PRIMARY KEY (strategy_id, run_id, shard_key);
		END IF;
	END $$`,
}

// ExternalSignalsSchemaDDL 是 quant.external_signals（schema + 表 + 消费索引）
// 的建表 DDL——migrate() 的 Migration 048 执行的就是这组语句（K8 / K7 遗留①）。
//
// 与 pkg/strategy.SignalSchemaDDL 的关系：那边是同一底层数组的别名（SPEC-R-01
// 已去重），EnsureSchema 仅作「独立可安装 / 测试自证」的幂等兜底入口。
// 顺序同 SignalSchemaDDL：schema 先于表，表先于索引。
var ExternalSignalsSchemaDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS quant`,
	`CREATE TABLE IF NOT EXISTS quant.external_signals (
		id           BIGSERIAL PRIMARY KEY,
		model_id     TEXT NOT NULL,
		symbol       TEXT NOT NULL,
		direction    TEXT NOT NULL,
		strength     DOUBLE PRECISION NOT NULL,
		as_of        TIMESTAMPTZ NOT NULL,
		content_hash TEXT NOT NULL,
		factors      JSONB,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT uq_external_signals_hash UNIQUE (content_hash)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_external_signals_lookup
		ON quant.external_signals (model_id, symbol, as_of)`,
}

// SupplyChainSchemaDDL 是产业链静态数据层四张表（quant schema）的建表 DDL——
// migrate() 的 Migration 049 执行的就是这组语句（P2-2，TASKS.md §五遗留待裁决）。
//
// 四张表统一带 source（来源标识）+ as_of（观察时点）溯源列，取代手抄快照；
// 每张表有幂等唯一约束（同键重导刷新行而不插新行）：
//   - sw_industry_tree   申万行业树（唯一键 code）
//   - supply_chain_edge  组件/零部件→整机/上下游映射（唯一键 upstream+downstream+relation）
//   - industry_vitality  行业景气指标（唯一键 industry_code+metric+obs_date+source）
//   - etf_constituent    ETF 成分（唯一键 etf_code+symbol+obs_date）
//
// 与 docs/migrations/049_add_supply_chain_tables.sql 是同源镜像（文档副本，
// 不执行）：两侧「归一化空白后逐条相等」由 pkg/storage 的一致性测试钉住
// （破坏腿：改任一处不改另一处 → 测试红）。顺序同 ExternalSignalsSchemaDDL：
// schema 先于表，表先于索引。
var SupplyChainSchemaDDL = []string{
	// quant schema 幂等：048 已建则本条为 no-op，顺延即可。
	`CREATE SCHEMA IF NOT EXISTS quant`,

	// 申万行业树。parent_code 为父行业 code，一级行业取空串（哨兵，
	// 不用 NULL——避免 IS NULL / = '' 两套比较口径）；is_leaf 由导入方
	// 按父子关系推导（无子节点即为叶子），不依赖上游 API 字段。
	`CREATE TABLE IF NOT EXISTS quant.sw_industry_tree (
		id          BIGSERIAL PRIMARY KEY,
		code        TEXT NOT NULL,
		parent_code TEXT NOT NULL DEFAULT '',
		level       INTEGER NOT NULL DEFAULT 1,
		name        TEXT NOT NULL,
		is_leaf     BOOLEAN NOT NULL DEFAULT FALSE,
		source      TEXT NOT NULL,
		as_of       TIMESTAMPTZ NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT uq_sw_industry_tree_code UNIQUE (code)
	)`,

	// 组件/零部件→整机/上下游映射。upstream/downstream 可以是 symbol 或
	// 行业 code，relation 说明语义（industry_member=申万行业成分，
	// symbol→行业 code；组件→整机等用各自取值）。weight 语义随 relation
	// 而定（成分关系恒 0——index_member 不含权重）。
	`CREATE TABLE IF NOT EXISTS quant.supply_chain_edge (
		id         BIGSERIAL PRIMARY KEY,
		upstream   TEXT NOT NULL,
		downstream TEXT NOT NULL,
		relation   TEXT NOT NULL,
		weight     DOUBLE PRECISION NOT NULL DEFAULT 0,
		source     TEXT NOT NULL,
		as_of      TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT uq_supply_chain_edge_triple UNIQUE (upstream, downstream, relation)
	)`,

	// 行业景气指标。obs_date 为指标观察日（DATE，PIT——不同观察日各自
	// 成行）；唯一键含 source：不同来源的同指标各行其行，不互相覆盖。
	`CREATE TABLE IF NOT EXISTS quant.industry_vitality (
		id            BIGSERIAL PRIMARY KEY,
		industry_code TEXT NOT NULL,
		metric        TEXT NOT NULL,
		obs_date      DATE NOT NULL,
		value         DOUBLE PRECISION NOT NULL,
		unit          TEXT NOT NULL DEFAULT '',
		source        TEXT NOT NULL,
		as_of         TIMESTAMPTZ NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT uq_industry_vitality_key UNIQUE (industry_code, metric, obs_date, source)
	)`,

	// ETF 成分。obs_date 为报告期（DATE，PIT——ETF 调仓后新旧报告期各自
	// 成行）；weight 为占净值比例（百分数，与 tushare fund_portfolio 的
	// stk_mkv_ratio 同口径）。
	`CREATE TABLE IF NOT EXISTS quant.etf_constituent (
		id        BIGSERIAL PRIMARY KEY,
		etf_code  TEXT NOT NULL,
		symbol    TEXT NOT NULL,
		weight    DOUBLE PRECISION NOT NULL DEFAULT 0,
		obs_date  DATE NOT NULL,
		source    TEXT NOT NULL,
		as_of     TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT uq_etf_constituent_key UNIQUE (etf_code, symbol, obs_date)
	)`,

	// 查询索引（对齐只读 API 的查询形态：按父+层级、按下游+关系、按行业+
	// 指标+日期、按 ETF+报告期）。
	`CREATE INDEX IF NOT EXISTS idx_sw_industry_tree_parent
		ON quant.sw_industry_tree (parent_code, level)`,
	`CREATE INDEX IF NOT EXISTS idx_supply_chain_edge_downstream
		ON quant.supply_chain_edge (downstream, relation)`,
	`CREATE INDEX IF NOT EXISTS idx_industry_vitality_lookup
		ON quant.industry_vitality (industry_code, metric, obs_date)`,
	`CREATE INDEX IF NOT EXISTS idx_etf_constituent_lookup
		ON quant.etf_constituent (etf_code, obs_date)`,
}

// MacroSeriesSchemaDDL 是宏观指标序列表 quant.macro_series（quant schema）的
// 建表 DDL——migrate() 的 Migration 050 执行的就是这组语句（P2-1，TASKS.md
// §五 遗留待裁决）。
//
// 国家层宏观观测（CPI/PPI/PMI/M2/社融/LPR/汇率，指标目录见 pkg/data/macro.go
// 的 macroIndicatorSpecs），与 SupplyChainSchemaDDL 的 industry_vitality
// （行业景气）并列不重复。统一带 source（来源标识）+ as_of（观察时点）
// 溯源列；唯一键 indicator+freq+obs_date+source 即幂等键（同键重导刷新行
// 而不插新行）。
//
// 与 docs/migrations/050_add_macro_series_table.sql 是同源镜像（文档副本，
// 不执行）：两侧「归一化空白后逐条相等」由 pkg/storage 的一致性测试钉住
// （破坏腿：改任一处不改另一处 → 测试红）。顺序同 SupplyChainSchemaDDL：
// schema 先于表，表先于索引。
var MacroSeriesSchemaDDL = []string{
	// quant schema 幂等：048/049 已建则本条为 no-op，顺延即可。
	`CREATE SCHEMA IF NOT EXISTS quant`,

	// 宏观指标序列。indicator 为指标代码（CPI_YOY/PPI_YOY/PMI/M2/M2_YOY/
	// TSF_INC/LPR_1Y/LPR_5Y/USDCNY_CLOSE）；freq 为频率（M=月/D=日）；
	// obs_date 为观察日（DATE，观察时点——月频取当月首日，日频取自然日；
	// fx_daily 的 trade_date 为 GMT 口径，原样入库不做时区转换，见
	// pkg/data/macro.go USDCNY_CLOSE 条目注释）；value 为观测值；
	// unit 为计量单位（%/亿元/点/汇率）。**可见/PIT 过滤锚点是 as_of（导入
	// 时刻），不是 obs_date**——观测日早于真实发布日，见
	// pkg/data/source/declarations.go 宏观声明（2026-10-16 用户裁决 A）。
	// 唯一键含 source：不同来源的
	// 同指标各行其行，同来源重导刷新取值（宏观指标会被重述修正）。
	`CREATE TABLE IF NOT EXISTS quant.macro_series (
		id         BIGSERIAL PRIMARY KEY,
		indicator  TEXT NOT NULL,
		freq       TEXT NOT NULL,
		obs_date   DATE NOT NULL,
		value      DOUBLE PRECISION NOT NULL,
		unit       TEXT NOT NULL DEFAULT '',
		source     TEXT NOT NULL,
		as_of      TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CONSTRAINT uq_macro_series_key UNIQUE (indicator, freq, obs_date, source)
	)`,

	// 查询索引（对齐只读 API 的查询形态：按指标+频率+日期区间）。
	`CREATE INDEX IF NOT EXISTS idx_macro_series_lookup
		ON quant.macro_series (indicator, freq, obs_date)`,
}
