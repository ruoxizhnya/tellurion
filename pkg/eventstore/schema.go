package eventstore

// ─── audit.message_log 的幂等建表能力 ─────────────────────────────
//
// 项目加表的唯一执行路径是 pkg/storage/postgres.go 的 migrate() 内联数组
// （见该文件中长达 40 行的取舍说明：golang-migrate 从未被调用，migrations/
// 只是历史记录）。SPEC-R-01 已把本包的建表 DDL 收编进 migrate()
// （Migration 054，文本在 storage.AuditSchemaDDL）；本包的 SchemaDDL 是它的
// 别名（同一底层数组），不再持有副本。
//
// EnsureSchema 保留为「独立可安装 / 测试自证」的幂等兜底入口：kernel Boot
// 的第一步会调用（蓝图 §4.2：EventStore.Init 必须先于一切，否则启动期消息无
// 审计丢失），且测试能独立把表建起来（CI 的 postgres 服务是空库），不依赖
// 「服务先跑过一遍 migrate」这种隐式前提。
//
// DDL 来源：contracts/kernel_modules.schema.sql 第 43-55 行（K0 冻结）。

import (
	"context"
	"fmt"
	"time"

	"tellurion/pkg/storage"
)

// SchemaDDL 是 audit schema + message_log 表 + 2 个索引的建表语句，为
// storage.AuditSchemaDDL 的别名（同一底层数组）——文本唯一真相在
// pkg/storage/postgres.go 的 migrate() 路径。
//
// 每条都必须幂等（IF NOT EXISTS）——EnsureSchema 会被反复调用（每个测试的
// setup、每次 Boot），不等价于 no-op 的语句迟早会炸在一次不该炸的启动上。
//
// 顺序有意义：schema 先于表，表先于索引。
var SchemaDDL = storage.AuditSchemaDDL

// ensureTimeout 建表是一次性动作，给它一个独立于单次 Append 的上限。
const ensureTimeout = 30 * time.Second

// EnsureSchema 幂等建立 audit schema 与 message_log 表。**对应 kernel Boot
// 的第 1 步**（EventStore.Init）：先开记录，后面 9 步发出的任何消息才有地方
// 落，否则启动期消息静默丢失。
//
// 调用时机由调用方决定，NewPGEventStore **不**隐式建表：构造函数做 I/O 会让
// 「连不上库」伪装成「构造失败」，而它本该是一条可被 Boot 顺序捕获的、有明确
// 语义的步骤；显式调用也让测试能复用一次安装。
func (s *PGEventStore) EnsureSchema(ctx context.Context) error {
	ctx, cancel := mergeTimeout(ctx, ensureTimeout)
	defer cancel()

	for i, ddl := range SchemaDDL {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("eventstore: EnsureSchema 第 %d 条 DDL 失败: %w", i+1, err)
		}
	}
	return nil
}
