CREATE TABLE IF NOT EXISTS ohlcv_minute (
    symbol      VARCHAR(20) NOT NULL,
    ts          TIMESTAMPTZ NOT NULL,
    open        DOUBLE PRECISION NOT NULL,
    high        DOUBLE PRECISION NOT NULL,
    low         DOUBLE PRECISION NOT NULL,
    close       DOUBLE PRECISION NOT NULL,
    volume      BIGINT,
    amount      DOUBLE PRECISION,
    source      VARCHAR(32) DEFAULT 'mootdx',
    ingest_time TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (symbol, ts)
);

CREATE INDEX IF NOT EXISTS idx_ohlcv_minute_symbol_ts ON ohlcv_minute (symbol, ts DESC);

CREATE TABLE IF NOT EXISTS stock_sector_map (
    symbol      VARCHAR(20) NOT NULL,
    sector_code VARCHAR(32) NOT NULL,
    sector_name VARCHAR(64) NOT NULL,
    source      VARCHAR(32) DEFAULT 'eastmoney',
    ingest_time TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (symbol, sector_code)
);

CREATE INDEX IF NOT EXISTS idx_ssm_sector ON stock_sector_map (sector_code);

CREATE TABLE IF NOT EXISTS market_activity (
    id              BIGSERIAL PRIMARY KEY,
    symbol          VARCHAR(20) NOT NULL,
    trade_date      DATE NOT NULL,
    turnover_rate   DOUBLE PRECISION,
    turnover_rate_f DOUBLE PRECISION,
    volume_ratio    DOUBLE PRECISION,
    source          VARCHAR(64) NOT NULL,
    as_of           TIMESTAMPTZ NOT NULL,
    CONSTRAINT market_activity_identity_present CHECK (
        btrim(symbol) <> '' AND btrim(source) <> ''
    ),
    UNIQUE (symbol, trade_date, source)
);

CREATE INDEX IF NOT EXISTS idx_market_activity_trade_date ON market_activity (trade_date);

-- 日线（CORE-CI-DB-LEGS-SKIPPED）：`pkg/marketdata` 的 1d 路由与
-- `pkg/expression` 的 `open/high/low/close/volume/turnover` 可用性探测都读
-- 裸表名 `ohlcv_daily_qfq`（生产部署由 search_path 指向 market）。
-- 形状镜像 tellurion-data:internal/schema/market.go 的 `market.ohlcv_daily_qfq`。
CREATE TABLE IF NOT EXISTS ohlcv_daily_qfq (
    symbol      VARCHAR(20) NOT NULL,
    trade_date  DATE NOT NULL,
    open        DOUBLE PRECISION NOT NULL,
    high        DOUBLE PRECISION NOT NULL,
    low         DOUBLE PRECISION NOT NULL,
    close       DOUBLE PRECISION NOT NULL,
    volume      DOUBLE PRECISION NOT NULL,
    turnover    DOUBLE PRECISION DEFAULT 0,
    trade_days  INT DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (symbol, trade_date)
);

-- `pkg/storage.ScreenFundamentals` 显式 JOIN `market.stocks`（schema 限定，
-- search_path 帮不上忙），所以这里连 schema 一起补。
-- 形状镜像 tellurion-data:internal/schema/market.go 的 `market.stocks`。
CREATE SCHEMA IF NOT EXISTS market;

CREATE TABLE IF NOT EXISTS market.stocks (
    symbol      VARCHAR(20) PRIMARY KEY,
    name        VARCHAR(200) NOT NULL,
    exchange    VARCHAR(20) NOT NULL,
    industry    VARCHAR(100),
    market_cap  DOUBLE PRECISION,
    list_date   DATE,
    delist_date DATE,
    status      VARCHAR(20) DEFAULT 'active',
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);
