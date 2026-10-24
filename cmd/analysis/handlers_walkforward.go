package main

import (
	"net/http"
	"tellurion/pkg/httpserver"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/backtest"
)

func registerWalkForwardRoutes(router *gin.Engine, wfEngine *backtest.WalkForwardEngine, logger zerolog.Logger) {
	wf := router.Group("/api/walkforward")
	{
		wf.POST("", runWalkForwardHandler(wfEngine, logger))
		wf.GET("/:strategy_id", getWalkForwardReportHandler(wfEngine))
		wf.GET("", listWalkForwardReportsHandler(wfEngine))
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

func getWalkForwardReportHandler(wfEngine *backtest.WalkForwardEngine) gin.HandlerFunc {
	return func(c *gin.Context) {
		strategyID := c.Param("strategy_id")

		report, err := wfEngine.GetLatestReport(c.Request.Context(), strategyID)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}
		if report == nil {
			httpserver.Fail(c, http.StatusNotFound, "no walk-forward report found")
			return
		}
		c.JSON(http.StatusOK, report)
	}
}

func listWalkForwardReportsHandler(wfEngine *backtest.WalkForwardEngine) gin.HandlerFunc {
	return func(c *gin.Context) {
		reports, err := wfEngine.ListReports(c.Request.Context(), 50)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"reports": reports, "count": len(reports)})
	}
}
