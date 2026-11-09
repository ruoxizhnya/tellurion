package main

// SVC-CORE-STRAT-RETIRE（2026-11-09）：策略定义的真相源是 tellurion-strategy
// （strategy.strategy_definitions + 版本链）。core 只保留**运行时注册表**的
// 只读视图：GET 列清单 / GET 取单条。写端点（POST / PUT / DELETE）随 DB 腿
// 一并退役——定义写入归 strategy 服务。
//
// 输出按 name 稳定排序：DefaultRegistry 内部是 map，直接序列化会让元素顺序
// 随进程变化，agent 侧做 diff 会误报。

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/tellurion-kit/httpserver"
	"tellurion/pkg/strategy"
)

func registerStrategyRoutes(router *gin.Engine) {
	router.GET("/api/strategies", func(c *gin.Context) {
		infos := strategy.DefaultRegistry.ListWithInfo()
		sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
		c.JSON(http.StatusOK, gin.H{"strategies": infos})
	})

	router.GET("/api/strategies/:id", func(c *gin.Context) {
		info, err := strategy.DefaultRegistry.Describe(c.Param("id"))
		if err != nil {
			httpserver.Fail(c, http.StatusNotFound, "strategy not found")
			return
		}
		c.JSON(http.StatusOK, info)
	})
}
