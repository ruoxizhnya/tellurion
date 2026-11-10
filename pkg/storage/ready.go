package storage

import (
	"context"
	"fmt"
)

// requiredRelations 是「本进程能把请求服务好」所依赖的最小关系集合
// （schema.table）。全部由启动 DDL（migrationStatements）产出：
//
//   - public.users        —— 鉴权 / 用户查询
//   - audit.message_log   —— 内核事件落审计（shutdown 也要写）
//   - run.backtest_results —— run 终态与报告读取
//   - quant.strategy_state —— 策略状态读写
//
// 就绪探针只在「池能 ping 通 + 这些表在」时算 ready。空库能 ping 通但
// 一条请求都服务不了（所有写路径 500），把它当 ready 会让负载均衡把流量
// 导进只会 5xx 的实例 —— 所以建表没跑到也要判不 ready。
var requiredRelations = []string{
	"public.users",
	"audit.message_log",
	"run.backtest_results",
	"quant.strategy_state",
}

// Ready 是**就绪**判据（区别于 Ping 的存活语义）：连接可用且启动 DDL 的
// 关键表存在。调用方应自带超时（探针场景 2s 量级），本方法不再叠加。
func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage: connection pool is not configured")
	}
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("storage: ping failed: %w", err)
	}
	for _, rel := range requiredRelations {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			"SELECT to_regclass($1) IS NOT NULL", rel).Scan(&exists); err != nil {
			return fmt.Errorf("storage: schema check %s failed: %w", rel, err)
		}
		if !exists {
			return fmt.Errorf("storage: required relation %s is missing "+
				"(startup DDL did not run or was rolled back)", rel)
		}
	}
	return nil
}
