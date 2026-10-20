package main

import (
	"tellurion/pkg/httpserver"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// CORS 中间件已收口到 internal/httpserver（P0-4）：cmd/analysis 与
// cmd/data 此前各有一份复制粘贴的版本，都硬编码 `*`。装配见
// setup.go 的 buildRouter。

type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitorInfo
	rate     int
	window   time.Duration
}

type visitorInfo struct {
	count   int
	resetAt time.Time
}

func newRateLimiter(rate int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		visitors: make(map[string]*visitorInfo),
		rate:     rate,
		window:   window,
	}
	// REV-23(3)：visitors 此前只增不消——每个来过的 ClientIP 永久滞留，
	// 与 pkg/bootstrap.RateLimiter（同款实现带每分钟 cleanup）行为分叉。
	// 对齐 bootstrap 模式：后台循环按 resetAt 过期删除。
	go rl.cleanup()
	return rl
}

// cleanupExpired 删除 resetAt 已过的 visitor（导出给测试直接驱动，
// 不用真的等一分钟——bootstrap 的 cleanup 是裸 sleep 循环，不可测）。
func (rl *rateLimiter) cleanupExpired(now time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for ip, v := range rl.visitors {
		if now.After(v.resetAt) {
			delete(rl.visitors, ip)
		}
	}
}

func (rl *rateLimiter) cleanup() {
	for {
		time.Sleep(time.Minute)
		rl.cleanupExpired(time.Now())
	}
}

func (rl *rateLimiter) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		rl.mu.Lock()
		now := time.Now()
		info, exists := rl.visitors[ip]
		if !exists || now.After(info.resetAt) {
			rl.visitors[ip] = &visitorInfo{count: 1, resetAt: now.Add(rl.window)}
			rl.mu.Unlock()
			c.Next()
			return
		}
		if info.count >= rl.rate {
			rl.mu.Unlock()
			httpserver.Fail(c, http.StatusTooManyRequests, "rate limit exceeded")
			c.Abort()
			return
		}
		info.count++
		rl.mu.Unlock()
		c.Next()
	}
}
