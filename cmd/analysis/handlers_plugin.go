package main

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"tellurion/pkg/auth"
	"tellurion/pkg/httpserver"
	"tellurion/pkg/strategy"
)

// registerPluginRoutes registers plugin management API routes.
//
// REV-08（安全）：读端点（列表 / 单个元信息 / 当前 watch 目录）保持全局
// 认证即可；**变更端点一律要求 admin**，理由：
//   - POST /load 与 POST /reload 把请求体里的路径交给 plugin.Open——Go
//     .so 的 init 代码在进程内直接执行，任何已认证角色都可调用等于给
//     viewer token 发了一把 RCE 钥匙；
//   - POST /watch-dir 改写路径校验的锚点（把目录设成 / 再 load 就绕过
//     前缀约束），必须与 load 同级把关；
//   - load-all / unload / :name/reload 同族同理。
//
// authSvc 为 nil（或鉴权关闭）时 RequireRole 是 no-op——开发态 open-access
// 的既有语义不变（与 execution 端点的 S7-P0-14 先例一致）。
func registerPluginRoutes(router *gin.Engine, loader *strategy.PluginLoader, authSvc *auth.Service) {
	if loader == nil {
		return
	}

	admin := router.Group("/api/plugins")
	if authSvc != nil {
		admin.Use(authSvc.RequireRole(auth.RoleAdmin))
	}

	// List all plugins
	router.GET("/api/plugins", func(c *gin.Context) {
		plugins := loader.List()
		c.JSON(http.StatusOK, gin.H{
			"plugins": plugins,
			"count":   len(plugins),
		})
	})

	// List active plugins
	router.GET("/api/plugins/active", func(c *gin.Context) {
		plugins := loader.ListActive()
		c.JSON(http.StatusOK, gin.H{
			"plugins": plugins,
			"count":   len(plugins),
		})
	})

	// Get specific plugin info
	router.GET("/api/plugins/:name", func(c *gin.Context) {
		name := c.Param("name")
		info, err := loader.Get(name)
		if err != nil {
			httpserver.Error(c, http.StatusNotFound, err)
			return
		}
		c.JSON(http.StatusOK, info)
	})

	// Load a plugin from file path
	admin.POST("/load", func(c *gin.Context) {
		var req struct {
			Path string `json:"path" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}

		info, err := loader.Load(req.Path)
		if err != nil {
			// REV-08：路径逃逸是客户端错误（400，报错点名目录）；其余
			// （目录未配置 / stat 失败 / plugin.Open 失败）维持 500。
			if errors.Is(err, strategy.ErrPluginPathEscape) {
				httpserver.Error(c, http.StatusBadRequest, err)
				return
			}
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "plugin loaded",
			"plugin":  info,
		})
	})

	// Unload a plugin by name
	admin.POST("/:name/unload", func(c *gin.Context) {
		name := c.Param("name")
		if err := loader.Unload(name); err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "plugin unloaded", "name": name})
	})

	// Reload a plugin by name
	admin.POST("/:name/reload", func(c *gin.Context) {
		name := c.Param("name")
		info, err := loader.ReloadByName(name)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "plugin reloaded",
			"plugin":  info,
		})
	})

	// Reload a plugin from file path
	admin.POST("/reload", func(c *gin.Context) {
		var req struct {
			Path string `json:"path" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}

		info, err := loader.Reload(req.Path)
		if err != nil {
			if errors.Is(err, strategy.ErrPluginPathEscape) {
				httpserver.Error(c, http.StatusBadRequest, err)
				return
			}
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "plugin reloaded",
			"plugin":  info,
		})
	})

	// Load all plugins from watch directory
	admin.POST("/load-all", func(c *gin.Context) {
		loaded, errs := loader.LoadAll()
		var errStrings []string
		for _, e := range errs {
			errStrings = append(errStrings, e.Error())
		}
		// REV-23(9)：状态码与结果挂钩，不再全失败也回 200——
		//   全成功 → 200；部分失败 → 207（Multi-Status，逐条看 errors）；
		//   全失败 → 500（失败原因经统一出口只进日志，客户端拿静态文案）。
		// 注意 loader.LoadAll 返回的 plugin 错误是策略文件加载失败，内容
		// 是路径/语法级别的（非凭据），207 的 errStrings 保留给运维看；
		// 500 腿则一律不回传，cause 只进日志。
		status := http.StatusOK
		message := "load all completed"
		switch {
		case len(loaded) == 0 && len(errs) > 0:
			httpserver.FailCause(c, http.StatusInternalServerError,
				"failed to load any plugin", errors.Join(errs...))
			return
		case len(errs) > 0:
			status = http.StatusMultiStatus
			message = "load all completed with errors"
		}
		c.JSON(status, gin.H{
			"message":  message,
			"loaded":   loaded,
			"errors":   errStrings,
			"count":    len(loaded),
			"errCount": len(errs),
		})
	})

	// Set watch directory
	admin.POST("/watch-dir", func(c *gin.Context) {
		var req struct {
			Dir string `json:"dir" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}

		// Resolve to absolute path
		absDir, err := filepath.Abs(req.Dir)
		if err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}

		if err := loader.SetWatchDir(absDir); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "watch directory set",
			"dir":     absDir,
		})
	})

	// Get watch directory
	router.GET("/api/plugins/watch-dir", func(c *gin.Context) {
		// Get watch dir from loader (we need to add a getter)
		c.JSON(http.StatusOK, gin.H{
			"watch_dir": getWatchDir(loader),
		})
	})
}

// getWatchDir extracts the watch directory from a PluginLoader.
func getWatchDir(loader *strategy.PluginLoader) string {
	return loader.WatchDir()
}
