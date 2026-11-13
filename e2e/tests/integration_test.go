package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	baseURL        = "http://localhost:8085"
	dataServiceURL = "http://localhost:8081"
	timeout        = 30 * time.Second
)

// ============================================================
// Integration Test Suite for Quant Trading System
// Tests all services via HTTP API (requires Docker Compose running)
// ============================================================

// S7-P0-8 (ODR-043-6): TestMain probes the analysis service before
// running any integration test. If the service is unreachable (Docker
// Compose not started), the entire suite is skipped so `go test ./...`
// exits 0 instead of FAIL. Set E2E_FORCE_SKIP=1 to skip unconditionally
// (useful in CI without Docker).
//
// ── AUD-52（2026-09-25 修）：这道门过去只探 /health，「服务在」于是被
// 当成了「服务能用」。但下面的用例要的是后者，两者是两件事 —— 结果两条
// 用例长期红，而且红得没有信息量（一条拨已退役的 :8084、一条打
// /api/strategies 拿到 401）。一条永远不可能绿的测试比没有测试更糟：
// 它会训练人忽略红色。
//
// 现在门与断言**同源** —— 判的三档正好是各用例真正会打的东西：
//
//	① /health 不通            → skip（环境没起，S7-P0-8 的原意）
//	② 通了，但 /api/execution/* 不存在 → **FAIL**。这是真回归：
//	  ODR-021 把 execution 并进 analysis，路由没了就是路由坏了。
//	③ 都在，但 /api/strategies 要鉴权 → **skip 并打印怎么改姿势**。
//	  这是环境**形态**不匹配，不是代码缺陷 —— 套件不带 token，而系统
//	  也没有首个管理员的引导可以拿 token（CreateUser 在 RequireRole(admin)
//	  后面，鸡生蛋）。报红只会变成长期噪声。
func TestMain(m *testing.M) {
	if os.Getenv("E2E_FORCE_SKIP") == "1" {
		fmt.Println("e2e: E2E_FORCE_SKIP=1, skipping integration tests")
		os.Exit(0)
	}

	st := probeIntegrationEnv()
	if !st.analysisUp {
		fmt.Println("e2e: analysis service not reachable, skipping integration tests " +
			"(start the stack with: tools/local-stack.sh start)")
		os.Exit(0)
	}
	if st.executionMissing {
		fmt.Fprintln(os.Stderr,
			"e2e: FAIL — analysis is up but the execution endpoints under "+
				baseURL+"/api/execution/* are missing (404 / unreachable).\n"+
				"     This is a real regression, not an environment problem: ODR-021\n"+
				"     merged risk+execution into analysis, so those routes must exist\n"+
				"     (cmd/analysis/handlers_execution.go RegisterRoutes).\n"+
				"     Note: the retired execution-service port used to be dialed here, which\n"+
				"     could never pass on this architecture (AUD-52).")
		os.Exit(1)
	}
	if st.authRequired {
		fmt.Fprintln(os.Stderr,
			"e2e: SKIP — analysis is up but REQUIRES AUTH, and this suite sends no token.\n"+
				"     The suite (like the whole e2e/ Playwright suite and the SPA) assumes an\n"+
				"     OPEN-ACCESS stack: AUTH_INSECURE=true + AUTH_INSECURE_EXPOSURE=loopback-published\n"+
				"     (see docker-compose.yml). There is deliberately no way to mint a token here:\n"+
				"     the first admin cannot be created through the API (chicken-and-egg).\n"+
				"     Running an auth-enabled stack is a POSITION MISMATCH, not a defect — skipped.")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// integrationEnv 是门判出来的环境形态（AUD-52）。
type integrationEnv struct {
	analysisUp       bool // /health 通了
	executionMissing bool // /api/execution/* 不存在（真回归）
	authRequired     bool // /api/strategies 或执行端点返回 401/403（形态不匹配）
}

// probeIntegrationEnv 用**下面用例真正会打的路径**判断环境，而不是只看 /health。
// 每条判据都能在对应用例里找到同源的断言（AUD-52 的要点）。
//
// 已知边界（写出来，免得被当成全覆盖）：鉴权开启时**所有** /api/* 都回 401，
// 包括根本不存在的路径（中间件挂在 router 上，先于路由匹配）—— 所以那种形态下
// 「执行端点是否存在」**判不出来**，只能判出「要鉴权」然后整体 skip。
// 也就是说这条检测只在 open-access 形态下有效 —— 那正是套件要跑的形态。
func probeIntegrationEnv() integrationEnv {
	var st integrationEnv
	if !servicesReachable() {
		return st
	}
	st.analysisUp = true

	client := &http.Client{Timeout: 3 * time.Second}

	// 执行端点：存在性判据是「不是 404 / 不是连不上」。开了鉴权时它会回 401，
	// 那同样证明**路由在**（只是进不去）—— 所以两种情形要分开记。
	code, err := probeStatus(client, http.MethodGet, baseURL+"/api/execution/account")
	switch {
	case err != nil || code == http.StatusNotFound:
		st.executionMissing = true
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		st.authRequired = true
	}

	// /api/strategies：用例断言 200，所以拿到 401/403 就是形态不匹配。
	if code, err := probeStatus(client, http.MethodGet, baseURL+"/api/strategies"); err == nil {
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			st.authRequired = true
		}
	}
	return st
}

// probeStatus 发一个只读请求，返回状态码。连不上时返回 (0, err)。
func probeStatus(client *http.Client, method, url string) (int, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// servicesReachable probes the analysis service health endpoint with a
// short timeout. Returns true if the service responds, false otherwise.
// S7-P0-8 (ODR-043-6).
//
// 注意它现在**只是门的第一步**（「环境起没起」），不再被当成「环境可用」——
// 后者由 probeIntegrationEnv 判（AUD-52）。
func servicesReachable() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestHealthCheck_AllServices(t *testing.T) {
	t.Run("Analysis Service Health", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/healthz")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var health map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&health)
		assert.Contains(t, health, "status")
	})

	t.Run("Data Service Health", func(t *testing.T) {
		resp, err := http.Get(dataServiceURL + "/healthz")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestBacktestAPI_EndToEnd(t *testing.T) {
	payload := map[string]interface{}{
		"strategy":        "momentum",
		"stock_pool":      []string{"600000.SH"},
		"start_date":      "2024-01-01",
		"end_date":        "2024-03-31",
		"initial_capital": 1000000,
	}

	body, _ := json.Marshal(payload)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(baseURL+"/api/backtest", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// CORE-E2E-FAIL-LOUD（2026-11-13）：非 200 不再「打一行日志就算过」。
	// 本用例发的就是 legacy 同步信封（strategy + stock_pool），按
	// CORE-BACKTEST-REQUEST-UNION 的判别必须走同步分支并 200；202 说明
	// 判别被改坏了（同步字段被路由到异步），同样要红。
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"POST /api/backtest（legacy 同步信封）必须 200；实测 %d，body=%s", resp.StatusCode, respBody)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(respBody, &result), "响应不是 JSON 对象: %s", respBody)

	// 终态与关键字段必须被断言，而不是只打印。
	assert.Equal(t, "completed", result["status"], "同步回测终态必须是 completed；body=%s", respBody)
	assert.Contains(t, result, "strategy", "结果缺 strategy 字段")
	assert.Contains(t, result, "total_return", "结果缺 total_return 字段")
	assert.Contains(t, result, "total_trades", "结果缺 total_trades 字段")
	t.Logf("✅ Sync backtest completed: strategy=%v total_return=%v total_trades=%v",
		result["strategy"], result["total_return"], result["total_trades"])
}

func TestStrategyAPI_ListStrategies(t *testing.T) {
	resp, err := http.Get(baseURL + "/api/strategies")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// ⚠️ 响应是**对象**不是裸数组：handlers_strategy.go 里是
	// `c.JSON(200, gin.H{"strategies": configs})`。此前这里 `Decode` 到
	// `[]map[string]interface{}`，解码必然失败 → strategies 恒为空 →
	// 每次都走「没有策略」那条分支 —— 断言看着在跑，其实一条都没检查（AUD-52）。
	var body struct {
		Strategies []map[string]interface{} `json:"strategies"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	// 这条是**正面证据**：内建策略至少要有 momentum（与
	// e2e/tests/api-strategy.spec.ts 断言一致），空列表不是「正常情况」。
	require.NotEmpty(t, body.Strategies, "内建策略列表不该为空（momentum 至少一个）")

	var names []string
	for _, s := range body.Strategies {
		for _, k := range []string{"name", "id", "strategy_id"} {
			if v, ok := s[k].(string); ok && v != "" {
				names = append(names, v)
				break
			}
		}
	}
	assert.NotEmpty(t, names, "策略条目必须带 name / id / strategy_id 之一")
	t.Logf("✅ %d strategies: %v", len(body.Strategies), names)
}

func TestOHLCVAPI_DataRetrieval(t *testing.T) {
	symbol := "600000.SH"
	startDate := "20240101"
	endDate := "20240115"

	url := fmt.Sprintf("%s/api/ohlcv/%s?start=%s&end=%s", baseURL, symbol, startDate, endDate)
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// CORE-E2E-FAIL-LOUD（2026-11-13）：以前非 200 只打一行
	// "may need data sync" 就算过 —— 数据没同步、路由写错、上游 500 一律
	// 看不出来。e2e 栈的前提就是 data-service 已同步 2024-01 的日线，
	// 所以这里必须 200 且非空；失败就点名原因。
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"GET /api/ohlcv/%s 必须 200（e2e 栈前提：data-service 已同步该区间日线）；实测 %d，body=%s",
		symbol, resp.StatusCode, respBody)

	var ohlcv []map[string]interface{}
	require.NoError(t, json.Unmarshal(respBody, &ohlcv), "响应不是 JSON 数组: %s", respBody)
	require.NotEmpty(t, ohlcv, "应该取到 %s 在 %s..%s 的日线；空数组说明同步没跑或区间无数据", symbol, startDate, endDate)

	firstBar := ohlcv[0]
	for _, k := range []string{"open", "high", "low", "close", "volume"} {
		assert.Contains(t, firstBar, k, "日线缺字段 %s", k)
	}
	t.Logf("✅ Retrieved %d bars for %s from %s to %s", len(ohlcv), symbol, startDate, endDate)
}

func TestFundamentalsAPI_DataRetrieval(t *testing.T) {
	symbol := "600000.SH"
	date := "20240930"

	// CORE-E2E-FAIL-LOUD（2026-11-13）：路径此前写成复数
	// `/api/fundamentals/:symbol`，真实路由是单数
	// `/api/fundamental/:symbol`（cmd/analysis/handlers_fundamental.go）——
	// 于是这条用例每次都 404，又被日志分支吞掉。同时把断言改成两步：
	// 200 ⇒ 必有 fundamental 对象；404 ⇒ 必须是业务空态机器码
	// `fundamental_not_found`（空表是合法状态，见 handler 注释）。
	url := fmt.Sprintf("%s/api/fundamental/%s?date=%s", baseURL, symbol, date)
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			Fundamental map[string]interface{} `json:"fundamental"`
		}
		require.NoError(t, json.Unmarshal(respBody, &body), "响应形状应为 {\"fundamental\":{...}}: %s", respBody)
		require.NotEmpty(t, body.Fundamental, "200 必须带非空 fundamental 对象；body=%s", respBody)
		t.Logf("✅ Fundamentals for %s on %s: %v", symbol, date, body.Fundamental)
	case http.StatusNotFound:
		assert.Contains(t, string(respBody), "fundamental_not_found",
			"404 必须是业务空态机器码，而不是路由缺失/解析错误；body=%s", respBody)
		t.Logf("ℹ️ 基本面表在该日期无记录（合法空态）: %s", respBody)
	default:
		t.Fatalf("GET /api/fundamental/%s 只接受 200 或 404(fundamental_not_found)；实测 %d，body=%s",
			symbol, resp.StatusCode, respBody)
	}
}

func TestFactorAPI_FactorComputation(t *testing.T) {
	// 2026-11-12 订正：该用例过去对任意非 200 只打日志 ⇒ 恒过（假绿）。
	// 目标语义其实很明确：因子求值不提供 HTTP 面（ADR-041 D1），`/api/factors/compute`
	// 从未注册过路由 ⇒ 必须 404；同族读面已退役 ⇒ 必须 410 + 机器码（ADR-037 §5）。
	date := "20240115"

	url := fmt.Sprintf("%s/api/factors/compute?date=%s&lookback=10", baseURL, date)
	resp, err := http.Post(url, "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"POST /api/factors/compute 必须不存在（无求值 HTTP 面）；实际 %d: %s", resp.StatusCode, string(body))

	getResp, err := http.Get(fmt.Sprintf("%s/api/factors/pe_ttm", baseURL))
	require.NoError(t, err)
	defer getResp.Body.Close()
	getBody, err := io.ReadAll(getResp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusGone, getResp.StatusCode,
		"GET /api/factors/:factor_name 必须 410 退役；实际 %d: %s", getResp.StatusCode, string(getBody))
	assert.Contains(t, string(getBody), "factor_http_surface_retired")
}

// TestExecutionService_OrderPersistence tests the new order persistence feature
//
// ⚠️ ODR-021 (P1-15)：execution 已**并入 analysis**，端点在
// :8085/api/execution/*（cmd/analysis/handlers_execution.go）。此前这里写死
// "http://localhost:8084" —— 那是**已退役的服务**，容器里根本没有 :8084，
// 所以这条测试在当前架构下永远不可能通过（AUD-52）。
//
// 请求体沿用 handler 的真实形状（createOrderRequest）：side / type，
// **不是** direction / order_type。
func TestExecutionService_OrderPersistence(t *testing.T) {
	orderPayload := map[string]interface{}{
		"symbol":   "600000.SH",
		"side":     "long",
		"type":     "market",
		"quantity": 1000,
		"price":    0,
	}

	body, _ := json.Marshal(orderPayload)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(baseURL+"/api/execution/orders", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	// 201 = 受理；400 = 领域层拒绝（休市 / 资金不足）—— 两者都证明请求抵达了
	// handler 并走完了绑定。401/403 到不了这里：TestMain 已经把鉴权形态拦下了。
	assert.Contains(t, []int{http.StatusCreated, http.StatusBadRequest}, resp.StatusCode,
		"订单端点必须可达且不被角色中间件拒绝（实测 %d）", resp.StatusCode)

	if resp.StatusCode == http.StatusCreated {
		var orderResult map[string]interface{}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&orderResult))

		assert.Contains(t, orderResult, "order_id")
		assert.Contains(t, orderResult, "symbol")
		assert.Equal(t, "600000.SH", orderResult["symbol"])
		t.Logf("✅ Order persisted: id=%v status=%v", orderResult["order_id"], orderResult["status"])
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		t.Logf("订单被领域层拒绝（400，端点本身正常）: %s", string(respBody))
	}
}

// TestDockerComposeServicesConnectivity verifies all services are reachable
//
// ⚠️ AUD-52：这张表此前写着 `:8083/risk/health` 与 `:8084/api/orders` ——
// 两个**已退役**的服务（ODR-021 把 risk + execution 并进 analysis，容器里
// 已无这两个端口）。而且 `/api/risk/health` 这个路径**从来没有存在过**
// （handlers_risk.go 只注册 calculate_position / detect_regime /
// check_stoploss / metrics）。于是每条都只打一行"⚠️ not reachable"，
// 既不是失败也不是信息 —— 正是 AUD-52 说的那种「看着在跑、其实没检查」。
func TestDockerComposeServicesConnectivity(t *testing.T) {
	// 左侧是**当前架构**：risk 与 execution 是 analysis 的 in-process 组件，
	// 不是独立服务（ODR-021 / P1-15）。
	//
	// CORE-E2E-FAIL-LOUD（2026-11-13）：此前「不是 404 就算通」把 5xx 也放过了 ——
	// 服务在崩、依赖没接都会显示 ✅。这里按端点显式声明可接受的状态码：
	//   - 探针：只接受 200；
	//   - 业务读面：200 或 401/403（鉴权开着时「端点存在但进不去」仍是连通证据）。
	// 其余（5xx / 404）一律失败。
	services := []struct {
		name    string
		url     string
		allowed []int
	}{
		{"analysis-service", baseURL + "/healthz", []int{http.StatusOK}},
		{"data-service", dataServiceURL + "/healthz", []int{http.StatusOK}},
		{"risk (in-process, ODR-021)", baseURL + "/api/risk/metrics",
			[]int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden}},
		{"execution (in-process, ODR-021)", baseURL + "/api/execution/account",
			[]int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden}},
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, svc := range services {
		svc := svc
		t.Run(svc.name, func(t *testing.T) {
			resp, err := client.Get(svc.url)
			require.NoError(t, err, "%s 不可达（%s）", svc.name, svc.url)
			defer resp.Body.Close()

			assert.Contains(t, svc.allowed, resp.StatusCode,
				"%s 返回 %d —— 不在显式允许集合 %v 内（404 = 表里路径写错；5xx = 服务真的不健康）",
				svc.name, resp.StatusCode, svc.allowed)
			t.Logf("✅ %s is responding (status %d)", svc.name, resp.StatusCode)
		})
	}
}

// Benchmark test for API response times
func BenchmarkAPI_HealthCheck(b *testing.B) {
	client := &http.Client{Timeout: timeout}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		resp, err := client.Get(baseURL + "/healthz")
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

// TestContext ensures tests run with proper context cancellation support
func TestMain_IntegrationSuite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Run("All Services Health Check", func(t *testing.T) {
		select {
		case <-ctx.Done():
			t.Fatal("timeout waiting for services")
		default:
			TestHealthCheck_AllServices(t)
		}
	})

	t.Run("Docker Compose Connectivity", func(t *testing.T) {
		select {
		case <-ctx.Done():
			t.Fatal("timeout checking connectivity")
		default:
			TestDockerComposeServicesConnectivity(t)
		}
	})
}
