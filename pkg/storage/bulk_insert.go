package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ruoxizhnya/tellurion-contracts/dataprotocol"
)

// UnifiedDataPoint is the storage-layer name for the shared point shape.
//
// It used to be a structurally compatible local copy, because storage could
// not import source (source → storage). Both sides now alias
// `tellurion-contracts:dataprotocol` (QT-40 X1a, contract v0.6.0), so the
// projection is an identity rather than a field-by-field copy that could
// silently drop a field.
type UnifiedDataPoint = dataprotocol.UnifiedDataPoint

// TableMapper resolves a data_type to (table_name, column layout).
//
// The storage layer is intentionally the single owner of the SQL schema
// details; adapters only need to set DataType correctly.
type TableMapper struct {
	// tables maps data_type → table name.
	tables map[string]string
	// pk maps data_type → primary-key columns in declaration order.
	pk map[string][]string
	// columns maps data_type → ordered list of column names written by
	// BulkInsert. Must include source/ingest_time when the table has them.
	columns map[string][]string
	// jsonbColumns lists columns whose values should be JSON-encoded from
	// the Data map. Empty slice means no JSONB columns.
	jsonbColumns map[string][]string
	// numericColumns lists columns that must be coerced to float64.
	numericColumns map[string][]string
}

// NewTableMapper constructs the default mapping for multi-source tables.
func NewTableMapper() *TableMapper {
	return &TableMapper{
		tables: map[string]string{
			"ohlcv_daily":    "ohlcv_daily_qfq",
			"ohlcv_minute":   "ohlcv_minute",
			"realtime_quote": "realtime_quote",
			"capital_flow":   "capital_flow",
			"sectors":        "sectors",
			"stock_sector":   "stock_sector_map",
			"top_list":       "top_list",
			"limit_up_pool":  "limit_up_pool",
			"announcements":  "announcements",
			"news":           "news",
			"hot_search":     "hot_search",
			"global_ohlcv":   "global_ohlcv",
			"fundamentals":   "stock_fundamentals",
		},
		pk: map[string][]string{
			"ohlcv_daily":    {"symbol", "trade_date"},
			"ohlcv_minute":   {"symbol", "ts"},
			"realtime_quote": {"symbol", "ts"},
			"capital_flow":   {"symbol", "trade_date", "period"},
			"sectors":        {"sector_code"},
			"stock_sector":   {"symbol", "sector_code"},
			"top_list":       {"trade_date", "symbol"},
			"limit_up_pool":  {"trade_date", "symbol"},
			"announcements":  {"ann_id"},
			"news":           {"news_id"},
			"hot_search":     {"rank", "snapshot_time"},
			"global_ohlcv":   {"symbol", "trade_date"},
			"fundamentals":   {"ts_code", "trade_date"},
		},
		columns: map[string][]string{
			"ohlcv_daily": {
				// DBT-01：与 ohlcv_daily_qfq 真实 DDL 逐列对齐（2026-10-17
				// information_schema 实测：symbol, trade_date, open, high, low,
				// close, volume, turnover, trade_days, created_at）。此前的
				// source / ingest_time 是**幻影列**——真库没有，ETL 每行 INSERT
				// 必报 42703（与 D2-R2 修掉的 fundamentals 幻影列同类）。其余
				// 9 列全在 DDL，无缺列；created_at 有 DEFAULT 惯例不写。
				"symbol", "trade_date", "open", "high", "low", "close",
				"volume", "turnover", "trade_days",
			},
			"ohlcv_minute": {
				"symbol", "ts", "open", "high", "low", "close",
				"volume", "amount", "source", "ingest_time",
			},
			"realtime_quote": {
				"symbol", "ts", "price", "open", "high", "low", "last_close",
				"volume", "amount", "bid1", "bid1_vol", "ask1", "ask1_vol",
				"bid2", "bid2_vol", "ask2", "ask2_vol",
				"bid3", "bid3_vol", "ask3", "ask3_vol",
				"bid4", "bid4_vol", "ask4", "ask4_vol",
				"bid5", "bid5_vol", "ask5", "ask5_vol",
				"source", "ingest_time",
			},
			"capital_flow": {
				"symbol", "trade_date", "period", "main_net", "main_buy_amount",
				"main_sell_amount", "super_net", "large_net", "medium_net",
				"small_net", "main_net_ratio", "retail_net", "retail_net_ratio",
				"close_price", "change_pct", "source", "ingest_time",
			},
			"sectors": {
				"sector_code", "sector_name", "category", "trade_date",
				"change_pct", "leading_symbol", "source", "ingest_time",
			},
			"stock_sector": {
				"symbol", "sector_code", "sector_name", "source", "ingest_time",
			},
			"top_list": {
				"trade_date", "symbol", "name", "net_buy", "buy_amount",
				"sell_amount", "turnover", "reason", "source", "ingest_time",
			},
			"limit_up_pool": {
				"trade_date", "symbol", "name", "limit_price", "first_time",
				"last_time", "limit_times", "continuous", "industry",
				"source", "ingest_time",
			},
			"announcements": {
				"ann_id", "symbol", "ann_title", "ann_time", "ann_type",
				"pdf_url", "source", "ingest_time",
			},
			"news": {
				"news_id", "symbol", "title", "content", "publish_time",
				"url", "source_name", "source", "ingest_time",
			},
			"hot_search": {
				"rank", "keyword", "snapshot_time", "heat", "source", "ingest_time",
			},
			"global_ohlcv": {
				"symbol", "trade_date", "open", "high", "low", "close",
				"volume", "adj_close", "source", "ingest_time",
			},
			"fundamentals": {
				// ann_date（披露日）必须落库（D2-R2）：stock_fundamentals.available_date
				// 是生成列 COALESCE(ann_date, trade_date)，而本路径的 trade_date 由
				// DataItem.TradeTime 喂入 = 报告期截止日（见 tushare_adapter.go
				// fetchFundamentals）——不写 ann_date，available_date 就退化成报告期
				// 截止日，三季报（9/30 截止、10/25 披露）会在 9/30 可见（前视）。
				// 列序与 DDL 对齐（ts_code, trade_date, ann_date, ...）。
				//
				// 不含 source/ingest_time：真库这张表没有这两列（2026-10-16
				// information_schema 实测；内联 DDL 同样没有），写上会让每一行
				// INSERT 直接 "column source does not exist" 失败——ETL 落库路径
				// 因此从未在真库跑通过。生产路径 SaveFundamentalBatch 同样不写
				// 这两列，口径一致。created_at / available_date 也不写：前者有
				// DEFAULT NOW()，后者是生成列（写了报错）。
				"ts_code", "trade_date", "ann_date", "pe", "pb", "ps", "roe", "roa",
				"debt_to_equity", "gross_margin", "net_margin", "revenue",
				"net_profit", "total_assets", "total_liab",
			},
		},
		jsonbColumns: map[string][]string{},
		numericColumns: map[string][]string{
			"ohlcv_daily":  {"open", "high", "low", "close", "volume", "turnover", "trade_days"},
			"ohlcv_minute": {"open", "high", "low", "close", "volume", "amount"},
			"realtime_quote": {
				"price", "open", "high", "low", "last_close", "volume", "amount",
				"bid1", "bid1_vol", "ask1", "ask1_vol",
				"bid2", "bid2_vol", "ask2", "ask2_vol",
				"bid3", "bid3_vol", "ask3", "ask3_vol",
				"bid4", "bid4_vol", "ask4", "ask4_vol",
				"bid5", "bid5_vol", "ask5", "ask5_vol",
			},
			"capital_flow": {
				"main_net", "main_buy_amount", "main_sell_amount", "super_net",
				"large_net", "medium_net", "small_net", "main_net_ratio",
				"retail_net", "retail_net_ratio", "close_price", "change_pct",
			},
			"sectors":       {"change_pct"},
			"top_list":      {"net_buy", "buy_amount", "sell_amount", "turnover"},
			"limit_up_pool": {"limit_price"},
			"hot_search":    {"heat"},
			"global_ohlcv":  {"open", "high", "low", "close", "volume", "adj_close"},
			"fundamentals": {
				"pe", "pb", "ps", "roe", "roa", "debt_to_equity", "gross_margin",
				"net_margin", "revenue", "net_profit", "total_assets", "total_liab",
			},
		},
	}
}

// TableFor returns the SQL table name for a given data type.
func (m *TableMapper) TableFor(dataType string) (string, bool) {
	t, ok := m.tables[dataType]
	return t, ok
}

// SupportedDataTypes returns a sorted list of supported data types.
func (m *TableMapper) SupportedDataTypes() []string {
	out := make([]string, 0, len(m.tables))
	for k := range m.tables {
		out = append(out, k)
	}
	return out
}

// BulkInsert writes a batch of UnifiedDataPoint values into the table
// appropriate for their DataType.
//
// Behavior:
//   - Points with unknown DataType are skipped and counted in `skipped`.
//   - ON CONFLICT updates all non-PK columns; the latest write wins per
//     (PK) — this is the desired semantics for daily refreshes.
//   - All inserts go through a single transaction for atomicity.
//   - Returns (persisted, skipped, error).
//
// Why per-row ON CONFLICT instead of staging+COPY? Volume per ETL run
// is bounded (typically a few thousand rows per data type) and the
// additional complexity of COPY is not justified at this scale. The
// pipeline is expected to be re-runnable, so idempotency is required.
func (s *PostgresStore) BulkInsert(ctx context.Context, dataType string, points []UnifiedDataPoint) (int, int, error) {
	if len(points) == 0 {
		return 0, 0, nil
	}

	table, ok := defaultTableMapper.TableFor(dataType)
	if !ok {
		return 0, len(points), fmt.Errorf("storage: unknown data type %q", dataType)
	}
	cols := defaultTableMapper.columns[dataType]
	pk := defaultTableMapper.pk[dataType]
	if len(cols) == 0 || len(pk) == 0 {
		return 0, len(points), fmt.Errorf("storage: missing schema for %q", dataType)
	}

	// Filter to points with a recognized data type
	valid := make([]UnifiedDataPoint, 0, len(points))
	skipped := 0
	for _, p := range points {
		if p.DataType == "" {
			p.DataType = dataType
		}
		if p.DataType != dataType {
			skipped++
			continue
		}
		if p.IngestTime.IsZero() {
			p.IngestTime = time.Now().UTC()
		}
		valid = append(valid, p)
	}
	if len(valid) == 0 {
		return 0, skipped, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, skipped, fmt.Errorf("bulk insert: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	persisted := 0
	batch := &pgx.Batch{}
	queued := 0
	for _, p := range valid {
		args, err := buildInsertArgs(p, cols, pk, defaultTableMapper.numericColumns[dataType])
		if err != nil {
			s.logger.Warn().Err(err).Str("symbol", p.Symbol).Str("data_type", dataType).
				Msg("skipping point with bad payload")
			skipped++
			continue
		}
		batch.Queue(buildUpsertSQL(table, cols, pk), args...)
		queued++
	}

	results := tx.SendBatch(ctx, batch)
	for i := 0; i < queued; i++ {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return persisted, skipped, fmt.Errorf("bulk insert: row %d: %w", i, err)
		}
		persisted++
	}
	if err := results.Close(); err != nil {
		return persisted, skipped, fmt.Errorf("bulk insert: close batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return persisted, skipped, fmt.Errorf("bulk insert: commit: %w", err)
	}

	s.logger.Info().
		Str("table", table).
		Str("data_type", dataType).
		Int("persisted", persisted).
		Int("skipped", skipped).
		Msg("bulk insert completed")

	return persisted, skipped, nil
}

// buildUpsertSQL constructs `INSERT INTO t (cols) VALUES (...) ON CONFLICT (...) DO UPDATE SET ...`
func buildUpsertSQL(table string, cols, pk []string) string {
	placeholders := ""
	for i := range cols {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += fmt.Sprintf("$%d", i+1)
	}
	pkList := ""
	for i, c := range pk {
		if i > 0 {
			pkList += ", "
		}
		pkList += c
	}
	updateSet := ""
	nonPK := make([]string, 0, len(cols))
	for _, c := range cols {
		isPK := false
		for _, p := range pk {
			if c == p {
				isPK = true
				break
			}
		}
		if !isPK {
			nonPK = append(nonPK, c)
		}
	}
	for i, c := range nonPK {
		if i > 0 {
			updateSet += ", "
		}
		updateSet += fmt.Sprintf("%s = EXCLUDED.%s", c, c)
	}
	return fmt.Sprintf(
		`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s`,
		table, joinCols(cols), placeholders, pkList, updateSet,
	)
}

func joinCols(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

// buildInsertArgs extracts values from a UnifiedDataPoint for the given columns.
//
// Standard column names ("symbol", "source", "ingest_time") are pulled
// from the typed fields; everything else is pulled from the Data map.
// Numeric columns are coerced to float64. Missing values become NULL.
func buildInsertArgs(p UnifiedDataPoint, cols []string, pk []string, numericCols []string) ([]interface{}, error) {
	numSet := make(map[string]struct{}, len(numericCols))
	for _, n := range numericCols {
		numSet[n] = struct{}{}
	}
	args := make([]interface{}, 0, len(cols))
	for _, c := range cols {
		var v interface{}
		switch c {
		case "symbol":
			v = p.Symbol
		case "ts_code":
			// Fundamentals use ts_code as the canonical ticker.
			if s, ok := p.Data["ts_code"].(string); ok {
				v = s
			} else {
				v = p.Symbol
			}
		case "trade_date":
			v = normalizeDate(p.TradeTime)
		case "ann_date":
			// 财报披露日（PIT 可见锚点，D2-R2）。tushare connector 的 wire
			// 投影（D2-R1）把 domain.Fundamental.AnnDate 原样放进 Data：
			// 类型是 *time.Time，**interface 持 typed nil ≠ nil**——default
			// 分支的 `raw == nil` 判不出来，nil 指针会原样漏给 pgx。虽然
			// pgx v5 的 derefPointerEncodePlan 恰好会把 nil 指针编码成
			// NULL，但那是隐式依赖驱动内部实现；且零值 time.Time（非指针）
			// 会被写成 0001-01-01，让 COALESCE 拿到公元前的值、整行从所有
			// 按 available_date 过滤的查询里消失（migration 025 清过一批
			// 这种脏数据）。故在此显式判空，口径与 trade_date / normalizeDate
			// 以及生产路径 SaveFundamentalBatch 一致：未披露 = NULL。
			av, err := normalizeAnnDate(p.Data["ann_date"])
			if err != nil {
				return nil, fmt.Errorf("column %s: %w", c, err)
			}
			v = av
		case "ts", "snapshot_time", "ann_time", "publish_time", "first_time", "last_time":
			v = p.TradeTime
		case "source":
			v = p.Source
		case "ingest_time":
			v = p.IngestTime
		default:
			raw, ok := p.Data[c]
			if !ok || raw == nil {
				v = nil
				break
			}
			if _, isNum := numSet[c]; isNum {
				f, err := toFloat64(raw)
				if err != nil {
					return nil, fmt.Errorf("column %s: %w", c, err)
				}
				v = f
			} else {
				v = raw
			}
		}
		args = append(args, v)
	}
	return args, nil
}

// normalizeDate converts a time.Time to a DATE-formattable value.
// We use the time.Time directly; pgx will encode it as timestamptz.
// The actual column type is DATE in most tables — pgx automatically
// truncates to date when the column is DATE.
func normalizeDate(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

// normalizeAnnDate coerces a Data["ann_date"] value into a DATE-encodable
// value for the stock_fundamentals.ann_date column (D2-R2).
//
// 语义（与 D2-R1 wire 投影、migration 025 脏数据清理、生产路径
// SaveFundamentalBatch 逐条对齐）：
//   - 键缺失 / 未披露（typed nil *time.Time）→ NULL；
//   - 零值 time.Time（0001-01-01）→ NULL（"没填"不是披露日）；
//   - 非零 → 日期值本身，由 available_date 生成列取 COALESCE。
//
// 其他类型返回错误：写入方把 ann_date 填成 string/float 是接线错误，
// fail-loud 让该行被 BulkInsert 跳过并计数，而不是把垃圾静默写进
// DATE 列（与 numericColumns 的强转失败同形）。
func normalizeAnnDate(raw interface{}) (interface{}, error) {
	switch t := raw.(type) {
	case nil:
		return nil, nil
	case *time.Time:
		if t == nil {
			return nil, nil
		}
		return normalizeDate(*t), nil
	case time.Time:
		return normalizeDate(t), nil
	default:
		return nil, fmt.Errorf("ann_date: unsupported type %T (want *time.Time / time.Time / absent)", raw)
	}
}

// toFloat64 coerces numeric JSON values to float64. JSON numbers come
// in as float64 already, but integer types from Go (e.g. int64) need
// promotion.
func toFloat64(v interface{}) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case int:
		return float64(x), nil
	case int32:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case uint:
		return float64(x), nil
	case uint32:
		return float64(x), nil
	case uint64:
		return float64(x), nil
	case json.Number:
		return x.Float64()
	case string:
		// String is sometimes used for integer IDs; refuse to coerce.
		return 0, fmt.Errorf("string %q is not numeric", x)
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}

// defaultTableMapper is the package-level mapper used by BulkInsert.
// It is intentionally not configurable at runtime: schema drift is a
// deploy-time concern, not a runtime knob.
//
// CR-51 (ODR-012) concurrency note:
// defaultTableMapper is read-only after process start. The TableMapper
// it wraps is itself concurrency-safe (NewTableMapper returns a
// value that is safe for concurrent map reads), so concurrent
// BulkInsert calls do NOT need a mutex around the mapper.
//
// What this means for future contributors:
//   - If you add a `Register(dataType, ...)` method that mutates the
//     underlying map, you MUST add a sync.RWMutex around the
//     defaultTableMapper and gate the mutation. The current code
//     path is read-only-by-design precisely so we don't pay for a
//     mutex on every BulkInsert call.
//   - If you need schema-different in tests, construct a fresh
//     TableMapper per test and pass it in via the table-mapper-aware
//     BulkInsert variant (TBD). Do not mutate defaultTableMapper.
var defaultTableMapper = NewTableMapper()

// BulkInserter is the persistence contract that any storage backend
// (Postgres, in-memory test double, future ClickHouse shim) must satisfy.
// CR-21 (ODR-012): defining this interface in pkg/storage (not in
// pkg/data/source) means consumers cannot accidentally call the wrong
// signature. Previously etl_test.go's stub used `[]interface{}` — Go's
// type system accepted the call site (because *PostgresStore was the
// concrete type) but the stub method would never have been invoked by
// the real code path, so the "test coverage" was an illusion. With this
// interface, any test double has to use the real `[]UnifiedDataPoint`
// signature to be assignable.
type BulkInserter interface {
	BulkInsert(ctx context.Context, dataType string, points []UnifiedDataPoint) (persisted int, skipped int, err error)
}
