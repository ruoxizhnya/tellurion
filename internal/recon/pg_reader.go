package recon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGReader 是 Reader 的 PostgreSQL 实现：只读、只 SELECT、不做任何 DDL/DML。
type PGReader struct {
	pool *pgxpool.Pool
}

// NewPGReader 建立一个只读用途的连接池。
func NewPGReader(ctx context.Context, dsn string) (*PGReader, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("recon: parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("recon: connect: %w", err)
	}
	return &PGReader{pool: pool}, nil
}

// Close 释放连接池。
func (r *PGReader) Close() { r.pool.Close() }

const describeQuery = `SELECT column_name
FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2
ORDER BY ordinal_position`

const primaryKeyQuery = `SELECT a.attname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
WHERE n.nspname = $1 AND c.relname = $2 AND i.indisprimary
ORDER BY k.ord`

// Read 读一张表的列集、主键、逐行摘要与列校验和。
func (r *PGReader) Read(ctx context.Context, t TableRef) (Snapshot, error) {
	cols, err := r.describe(ctx, t)
	if err != nil {
		return Snapshot{}, err
	}
	if len(cols) == 0 {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrTableMissing, t)
	}
	pk, err := r.primaryKey(ctx, t)
	if err != nil {
		return Snapshot{}, err
	}
	if len(pk) == 0 {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNoPrimaryKey, t)
	}

	digests, err := r.rowDigests(ctx, t, pk)
	if err != nil {
		return Snapshot{}, err
	}
	checksums, err := r.columnChecksums(ctx, t, cols, pk)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Exists:          true,
		Columns:         cols,
		PrimaryKey:      pk,
		RowDigests:      digests,
		ColumnChecksums: checksums,
	}, nil
}

func (r *PGReader) describe(ctx context.Context, t TableRef) ([]string, error) {
	rows, err := r.pool.Query(ctx, describeQuery, t.Schema, t.Table)
	if err != nil {
		return nil, mapPGError(t, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, mapPGError(t, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPGError(t, err)
	}
	return out, nil
}

func (r *PGReader) primaryKey(ctx context.Context, t TableRef) ([]string, error) {
	rows, err := r.pool.Query(ctx, primaryKeyQuery, t.Schema, t.Table)
	if err != nil {
		return nil, mapPGError(t, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, mapPGError(t, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPGError(t, err)
	}
	return out, nil
}

func (r *PGReader) rowDigests(ctx context.Context, t TableRef, pk []string) (map[string]string, error) {
	q := rowDigestQuery(t, pk)
	if err := assertReadOnlySQL(q); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, mapPGError(t, err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var key, digest string
		if err := rows.Scan(&key, &digest); err != nil {
			return nil, mapPGError(t, err)
		}
		out[key] = digest
	}
	if err := rows.Err(); err != nil {
		return nil, mapPGError(t, err)
	}
	return out, nil
}

func (r *PGReader) columnChecksums(ctx context.Context, t TableRef, cols, pk []string) (map[string]string, error) {
	q := columnChecksumQuery(t, cols, pk)
	if err := assertReadOnlySQL(q); err != nil {
		return nil, err
	}
	dest := make([]any, len(cols))
	for i := range cols {
		var v string
		dest[i] = &v
	}
	row := r.pool.QueryRow(ctx, q)
	if err := row.Scan(dest...); err != nil {
		return nil, mapPGError(t, err)
	}
	out := make(map[string]string, len(cols))
	for i, c := range cols {
		out[c] = *dest[i].(*string)
	}
	return out, nil
}

// rowDigestQuery 按主键生成逐行摘要查询。分隔符用 chr(31)（单元分隔符），避免拼接歧义。
func rowDigestQuery(t TableRef, pk []string) string {
	parts := make([]string, 0, len(pk))
	for _, c := range pk {
		parts = append(parts, "t."+quoteIdent(c)+"::text")
	}
	return fmt.Sprintf(
		"SELECT %s AS recon_key, md5(row_to_json(t)::text) AS recon_digest FROM %s AS t",
		strings.Join(parts, " || chr(31) || "),
		qualified(t),
	)
}

// columnChecksumQuery 一次扫描算出每列的 md5 校验和（按主键排序保证稳定）。
// NULL 用固定哨兵 `__NULL__`，避免与真实字符串撞车。
func columnChecksumQuery(t TableRef, cols, pk []string) string {
	order := make([]string, 0, len(pk))
	for _, c := range pk {
		order = append(order, "t."+quoteIdent(c))
	}
	selects := make([]string, 0, len(cols))
	for _, c := range cols {
		selects = append(selects, fmt.Sprintf(
			"COALESCE(md5(string_agg(md5(COALESCE(t.%s::text, '__NULL__')), '' ORDER BY %s)), '')",
			quoteIdent(c), strings.Join(order, ", ")))
	}
	return fmt.Sprintf("SELECT %s FROM %s AS t", strings.Join(selects, ", "), qualified(t))
}

func qualified(t TableRef) string {
	if t.Schema == "" {
		return quoteIdent(t.Table)
	}
	return quoteIdent(t.Schema) + "." + quoteIdent(t.Table)
}

// quoteIdent 是 PostgreSQL 标识符引用：双写双引号。表名/列名来自 catalog，但仍一律引用。
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// mapPGError 把 PG 错误折成可判别的哨兵；其余原样返回（超时靠 errors.Is 判定）。
func mapPGError(t TableRef, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42P01", "3F000": // undefined_table / invalid_schema_name
			return fmt.Errorf("%w: %s", ErrTableMissing, t)
		case "42501": // insufficient_privilege
			return fmt.Errorf("%w: %s", ErrPermissionDenied, t)
		}
	}
	return err
}
