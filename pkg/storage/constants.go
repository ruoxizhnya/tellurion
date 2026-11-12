package storage

import "time"

// Database connection pool constants
const (
	// PostgresMaxConns is the maximum number of connections in the PostgreSQL pool
	PostgresMaxConns = 20

	// PostgresMinConns is the minimum number of idle connections in the PostgreSQL pool
	PostgresMinConns = 5

	// PostgresConnMaxLifetime is the maximum lifetime of a connection before it's closed
	PostgresConnMaxLifetime = time.Hour

	// PostgresConnMaxIdleTime is the maximum time a connection can sit idle before being closed
	PostgresConnMaxIdleTime = 30 * time.Minute
)

// Batch operation limits
const (
	// DefaultBatchSize is the default batch size for bulk database operations
	DefaultBatchSize = 1000
)
