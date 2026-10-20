package main

// REV-10（网关侧）：analysis 反代转发 SSE 时，网关自身的 WriteTimeout
// （60s）同样是**绝对**写截止——长作业的进度流经网关必被掐断。本测试把
// 超时按比例缩到 1s，断言经真反代读到的流能活过 2.5s。

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncProxy_SSE_SurvivesGatewayWriteTimeout(t *testing.T) {
	// 上游：每 200ms 一帧 progress，共 3s。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		if !ok {
			return
		}
		for i := 0; i < 15; i++ {
			fmt.Fprint(w, "event: progress\ndata: {\"n\":1}\n\n")
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}))
	defer upstream.Close()

	v := viper.New()
	v.Set("data_service.url", upstream.URL)

	r := gin.New()
	registerProxyRoutes(r, &http.Client{Timeout: 10 * time.Second}, v, zerolog.Nop())

	srv := httptest.NewUnstartedServer(r)
	srv.Config.WriteTimeout = 1 * time.Second // 远小于观察窗口（2.5s）
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sync/jobs/x/progress", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	events := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "event:") && strings.Contains(sc.Text(), "progress") {
			events++
		}
		if time.Since(start) >= 2500*time.Millisecond && events >= 3 {
			break
		}
	}
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 2500*time.Millisecond,
		"网关侧流在 %.2fs 就断了——WriteTimeout(1s) 掐断了 SSE（REV-10 网关腿回归）", elapsed.Seconds())
	assert.GreaterOrEqual(t, events, 3, "应至少收到 3 个 progress 帧（200ms 一帧）")
	assert.NoError(t, sc.Err())
}
