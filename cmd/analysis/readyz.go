package main

// readyz.go —— 就绪探针（/readyz、/api/readyz）。
//
// 与 /health 的分工必须说清楚，否则两个探针迟早会退化成同一个：
//
//	/health  存活（liveness）：进程还在、HTTP 栈没死。**不碰数据库**——
//	         库短暂不可用时重启进程只会让在途请求陪葬，也修不好库。
//	/readyz  就绪（readiness）：**现在能不能接流量**。判据是池 ping 通
//	         且启动 DDL 的关键表在位；库断开或建表没跑到 → 503，由
//	         负载均衡把实例摘出去，等库回来再自动恢复。
//
// 两条路径都注册：/readyz 给 k8s / systemd / 裸机探针，/api/readyz 与
// /api/health 的别名口径一致（前端 dev 只代理 /api，运维脚本也常按 /api 前缀写）。

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"tellurion/pkg/storage"
)

// readinessTimeout 是单次就绪判定的预算。探针周期通常是秒级，2s 足够覆盖
// 一次 TCP 重连 + 4 次 to_regclass；再长会让探针本身拖垮发布节奏。
const readinessTimeout = 2 * time.Second

// registerReadiness 注册就绪探针。store 允许为 nil（未接库的运行形态）：
// 此时按「未就绪」处理而不是 panic —— 一个没接库的 analysis-service
// 确实不该接流量。
func registerReadiness(router *gin.Engine, store *storage.PostgresStore, logger zerolog.Logger) {
	handler := func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), readinessTimeout)
		defer cancel()

		if err := store.Ready(ctx); err != nil {
			logger.Warn().Err(err).Msg("readiness check failed")
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status":    "not_ready",
				"service":   "analysis-service",
				"checks":    gin.H{"database": err.Error()},
				"timestamp": time.Now().Format(time.RFC3339),
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":    "ready",
			"service":   "analysis-service",
			"checks":    gin.H{"database": "ok", "schema": "ok"},
			"timestamp": time.Now().Format(time.RFC3339),
		})
	}
	router.GET("/readyz", handler)
	router.GET("/api/readyz", handler)
}
