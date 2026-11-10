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
