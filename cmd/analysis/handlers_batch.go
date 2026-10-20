package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"tellurion/pkg/backtest"
	"tellurion/pkg/httpserver"
)

// REV-11 资源上界（SPEC 批量接口节同步了这三个值）：
const (
	// maxBatchConcurrency：单请求并发上限。再大也不会更快（瓶颈是回测本身
	// 与 DB），只会让一个客户端请求独占共享进程的 goroutine 预算。
	maxBatchConcurrency = 16
	// maxBatchTasks：单请求任务数上限。
	maxBatchTasks = 100
	// batchRunTimeout：整批的 ctx 预算（单个回测的同步预算是 10min）。
	batchRunTimeout = 30 * time.Minute
)

func registerBatchRoutes(router *gin.Engine, batchEngine *backtest.BatchEngine, logger zerolog.Logger) {
	api := router.Group("/api/batch")
	{
		api.POST("", runBatchHandler(batchEngine, logger))
		api.GET("/:batch_id", getBatchReportHandler(batchEngine))
		api.GET("/:batch_id/export/:format", exportBatchReportHandler(batchEngine, logger))
	}
}

func runBatchHandler(batchEngine *backtest.BatchEngine, logger zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req batchRunRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Wrap(c, http.StatusBadRequest, err, "invalid request: ")
			return
		}

		if len(req.Tasks) == 0 {
			httpserver.Fail(c, http.StatusBadRequest, "no tasks provided")
			return
		}

		// REV-11：资源上界（客户端可控的值必须有天花板）。
		// 并发封顶 16：单请求最多 16 个 worker，防「一个 batch 请求把共享
		// 引擎的 goroutine 预算吃光」；任务封顶 100：一次批量最多 100 个
		// 回测（远超真实用法：多策略 × 多周期通常是十几个）。
		if len(req.Tasks) > maxBatchTasks {
			httpserver.Fail(c, http.StatusBadRequest,
				fmt.Sprintf("too many tasks: %d > %d", len(req.Tasks), maxBatchTasks))
			return
		}
		if req.Config.Concurrency > maxBatchConcurrency {
			httpserver.Fail(c, http.StatusBadRequest,
				fmt.Sprintf("concurrency too high: %d > %d", req.Config.Concurrency, maxBatchConcurrency))
			return
		}

		config := backtest.DefaultBatchConfig()
		if req.Config.Concurrency > 0 {
			config.Concurrency = req.Config.Concurrency
		}
		if req.Config.RunWF {
			config.RunWF = req.Config.RunWF
		}
		if req.Config.WFTrainDays > 0 {
			config.WFTrainDays = req.Config.WFTrainDays
		}
		if req.Config.WFTestDays > 0 {
			config.WFTestDays = req.Config.WFTestDays
		}
		if req.Config.WFStepDays > 0 {
			config.WFStepDays = req.Config.WFStepDays
		}
		// REV-11：ctx 加超时。批量语义 = 最多 100 个单回测，故不套用同步
		// 回测的 10min（那是**单个**回测的预算），按 30min 上限成文；到点
		// 由 ctx 取消，workers 停止取新任务、在跑的随 ctx 退出。
		ctx, cancel := context.WithTimeout(c.Request.Context(), batchRunTimeout)
		defer cancel()
		report, err := batchEngine.Run(ctx, req.Tasks, config)
		if err != nil {
			logger.Error().Err(err).Int("tasks", len(req.Tasks)).Msg("Batch backtest failed")
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, report)
	}
}

func getBatchReportHandler(batchEngine *backtest.BatchEngine) gin.HandlerFunc {
	return func(c *gin.Context) {
		batchID := c.Param("batch_id")
		report, err := batchEngine.GetReport(batchID)
		if err != nil {
			httpserver.Fail(c, http.StatusNotFound, "batch report not found")
			return
		}
		c.JSON(http.StatusOK, report)
	}
}

func exportBatchReportHandler(batchEngine *backtest.BatchEngine, logger zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		batchID := c.Param("batch_id")
		format := c.Param("format")

		report, err := batchEngine.GetReport(batchID)
		if err != nil {
			httpserver.Fail(c, http.StatusNotFound, "batch report not found")
			return
		}

		switch format {
		case "json":
			c.Header("Content-Type", "application/json")
			c.Header("Content-Disposition", "attachment; filename=batch_"+batchID+".json")
			c.JSON(http.StatusOK, report)
		case "csv":
			// REV-23(8)：此分支原来头报 text/csv、体回 JSON——谎报
			// content-type。CSV 序列化尚未实现，如实回 501（Not
			// Implemented）并把可用出口指给客户端，不再伪造响应头。
			httpserver.Fail(c, http.StatusNotImplemented,
				"CSV export not implemented; use format=json")
		default:
			httpserver.Failf(c, http.StatusBadRequest, "unsupported format: %s", format)
		}
	}
}

type batchRunRequest struct {
	Tasks  []backtest.BatchTask `json:"tasks" binding:"required,min=1"`
	Config batchConfigRequest   `json:"config"`
}

type batchConfigRequest struct {
	Concurrency int  `json:"concurrency"`
	RunWF       bool `json:"run_walk_forward"`
	WFTrainDays int  `json:"wf_train_days"`
	WFTestDays  int  `json:"wf_test_days"`
	WFStepDays  int  `json:"wf_step_days"`
}
