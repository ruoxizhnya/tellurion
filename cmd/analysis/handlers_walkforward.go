package main

import (
	"github.com/ruoxizhnya/tellurion-kit/httpserver"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/backtest"
)

// registerWalkForwardRoutes 注册 walk-forward 计算端点。
//
// SVC-CORE-RUN-RETIRE（2026-11-09）：walk_forward_reports 持久化退役后，
// 原先的两个读端点（GET /api/walkforward、GET /api/walkforward/:strategy_id）
// 随表一起删除；POST 保留为**纯计算**端点 —— 执行分段回测并直接返回报告，
// 不写库。agent 的 walk-forward 工具走的正是这条 POST。
func registerWalkForwardRoutes(router *gin.Engine, wfEngine *backtest.WalkForwardEngine, logger zerolog.Logger) {
	wf := router.Group("/api/walkforward")
	{
		wf.POST("", runWalkForwardHandler(wfEngine, logger))
	}
}

func runWalkForwardHandler(wfEngine *backtest.WalkForwardEngine, logger zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req backtest.WalkForwardRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Wrap(c, http.StatusBadRequest, err, "invalid request: ")
			return
		}

		// QT-68：strategy_params 与同步回测共用 QT-20 的两层校验。
		//   ① 形状（嵌套对象/数组拒绝）由 contracts 的 ValidateRunSpec 判；
		//   ② 未知键 / 类型 / 范围由策略清单（manifest.params[]）判。
		// 必须在启动窗口之前拒绝：runSingleWindow 对单个窗口的失败是
		// Warn + 跳过，否则非法参数会伪装成「所有窗口都失败」（500）。
		if err := (contracts.BacktestRequest{
			Strategy:       req.Strategy,
			StrategyParams: req.StrategyParams,
		}).ValidateRunSpec(); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "invalid strategy_params: "+err.Error())
			return
		}
		if _, err := backtest.ResolveStrategyParams(req.Strategy, req.StrategyParams); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, err.Error())
			return
		}

		if req.WalkForwardParams.TrainDays <= 0 {
			req.WalkForwardParams.TrainDays = 250
		}
		if req.WalkForwardParams.TestDays <= 0 {
			req.WalkForwardParams.TestDays = 60
		}
		if req.WalkForwardParams.StepDays <= 0 {
			req.WalkForwardParams.StepDays = 60
		}
		if req.WalkForwardParams.MinTrainDays <= 0 {
			req.WalkForwardParams.MinTrainDays = req.WalkForwardParams.TrainDays / 2
		}

		report, err := wfEngine.RunWalkForward(c.Request.Context(), req)
		if err != nil {
			logger.Error().Err(err).Str("strategy", req.Strategy).Msg("Walk-forward validation failed")
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, report)
	}
}
