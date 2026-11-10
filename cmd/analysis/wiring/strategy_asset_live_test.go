package wiring

// strategy_asset_live_test.go —— QT-32「真资产服务端到端」的 opt-in 集成用例。
//
// 与 strategy_asset_test.go 的 httptest 替身互补：替身证明闸门对状态机的映射，
// 这里用**真的 assetservice 进程**（内存 Store、回环开放）走完整链路
// create → source → 登记 artifact → validate → promote → resolve/audit，
// 再让同一个 `CheckStrategyRef` 判定放行 / 拒绝。
//
// 开关是 `ASSET_SERVICE_URL`（例如 http://127.0.0.1:18090）；未设置即 skip——
// CI 没有资产服务时保持绿。设置后服务不可达 / 链路失败一律 fail，不降级成 skip。
//
// 复现（assetservice 侧）：
//
//	go build -o e2e-asset ./cmd/assetservice          # cwd tellurion-strategy
//	./e2e-asset -addr 127.0.0.1:18090                 # 无 -dsn / 无 -jwt-secret
//	ASSET_SERVICE_URL=http://127.0.0.1:18090 go test ./cmd/analysis/wiring -run TestStrategyAssetGate_LiveAssetService -v

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// liveAssetWasm 造一个最小的合法 wasm：magic + version + import section。
// 与 assetservice 的 Verify 同口径——imports 从字节里解析，不在元数据里自证。
func liveAssetWasm(imports ...string) []byte {
	var payload []byte
	payload = liveAppendULEB(payload, uint64(len(imports)))
	for _, imp := range imports {
		module, field, ok := strings.Cut(imp, ".")
		if !ok {
			module, field = imp, "fn"
		}
		payload = liveAppendName(payload, module)
		payload = liveAppendName(payload, field)
		payload = append(payload, 0x00)      // kind: func
		payload = liveAppendULEB(payload, 0) // typeidx
	}
	out := []byte("\x00asm")
	var version [4]byte
	binary.LittleEndian.PutUint32(version[:], 1)
	out = append(out, version[:]...)
	out = append(out, 2) // import section
	out = liveAppendULEB(out, uint64(len(payload)))
	return append(out, payload...)
}

func liveAppendULEB(dst []byte, v uint64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		dst = append(dst, b)
		if v == 0 {
			return dst
		}
	}
}

func liveAppendName(dst []byte, s string) []byte {
	dst = liveAppendULEB(dst, uint64(len(s)))
	return append(dst, s...)
}

type liveAssetClient struct {
	t      *testing.T
	base   string
	client *http.Client
}

func (c *liveAssetClient) do(method, path string, body any, headers map[string]string) (int, []byte) {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		c.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v (asset service at %s is unreachable)", method, path, err, c.base)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, raw
}

func (c *liveAssetClient) want(method, path string, body any, headers map[string]string, wantStatus int) map[string]any {
	c.t.Helper()
	status, raw := c.do(method, path, body, headers)
	if status != wantStatus {
		c.t.Fatalf("%s %s = %d, want %d (body: %s)", method, path, status, wantStatus, raw)
	}
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			c.t.Fatalf("decode %s %s: %v (body: %s)", method, path, err, raw)
		}
	}
	return out
}

// TestStrategyAssetGate_LiveAssetService 用真资产服务证明「promoted + 审计才放行」。
func TestStrategyAssetGate_LiveAssetService(t *testing.T) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("ASSET_SERVICE_URL")), "/")
	if base == "" {
		t.Skip("ASSET_SERVICE_URL is empty — live asset-service end-to-end test skipped (see file header for the reproduction command)")
	}

	gate, err := NewStrategyAssetGate(base, "", 10*time.Second)
	if err != nil {
		t.Fatalf("NewStrategyAssetGate(%q): %v", base, err)
	}
	c := &liveAssetClient{t: t, base: base, client: &http.Client{Timeout: 10 * time.Second}}
	ctx := context.Background()

	// 1. 建定义（carrier=wasm，scope=market）。
	created := c.want(http.MethodPost, "/api/strategies", map[string]any{
		"carrier":     "wasm",
		"abi_version": "on_bar/2",
		"scope":       "market",
		"entry":       "OnBar",
		"capability": map[string]any{
			"frequencies": []string{"1d"},
			"markets":     []string{"A"},
			"features":    []string{"momentum"},
		},
	}, nil, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %v", created)
	}

	// 2. 两个版本：v1 走完整的 promote，v2 只到 validated（闸门必须拒绝）。
	putSource := func(source string) string {
		out := c.want(http.MethodPut, "/api/strategies/"+id+"/source",
			map[string]string{"source": source}, nil, http.StatusOK)
		hash, _ := out["version_hash"].(string)
		if len(hash) != 64 {
			t.Fatalf("version_hash = %q, want 64 hex", hash)
		}
		return hash
	}
	v1 := putSource("package strategy\nfunc OnBar() { /* live e2e v1 */ }")
	v2 := putSource("package strategy\nfunc OnBar() { /* live e2e v2 */ }")

	register := func(version string, wasm []byte, toolchain string) (artifactHash string) {
		sum := sha256.Sum256(wasm)
		hash := hex.EncodeToString(sum[:])
		// imports 留空：服务端从字节的 import section 回填（元数据不参与自证）。
		out := c.want(http.MethodPost, "/internal/strategies/"+id+"/artifacts", map[string]any{
			"strategy_id":        id,
			"version_hash":       version,
			"artifact_hash":      hash,
			"carrier":            "wasm",
			"abi_version":        "on_bar/2",
			"toolchain":          toolchain,
			"size_bytes":         len(wasm),
			"memory_limit_bytes": 1 << 20,
			"bytes_base64":       base64.StdEncoding.EncodeToString(wasm),
		}, nil, http.StatusCreated)
		if out["status"] != "built" || out["artifact_hash"] != hash {
			t.Fatalf("register = %v, want status=built artifact_hash=%s", out, hash)
		}
		return hash
	}
	wasmV1 := liveAssetWasm("env.get_bar", "env.emit_target2")
	artV1 := register(v1, wasmV1, "live-e2e-a")
	artV2 := register(v2, liveAssetWasm("env.get_bar"), "live-e2e-b")
	if artV2 == artV1 {
		t.Fatalf("v1/v2 artifact hashes must differ (both %s)", artV1)
	}

	validate := func(version, toolchain string) {
		out := c.want(http.MethodPost, "/api/strategies/"+id+"/versions/"+version+"/validate",
			map[string]string{"toolchain": toolchain}, nil, http.StatusOK)
		if out["status"] != "validated" {
			t.Fatalf("validate %s = %v, want status=validated", version, out)
		}
	}
	validate(v1, "live-e2e-a")
	validate(v2, "live-e2e-b")

	c.want(http.MethodPost, "/api/strategies/"+id+"/versions/"+v1+"/promote",
		map[string]string{"toolchain": "live-e2e-a"},
		map[string]string{"X-Actor": "qt32-live-e2e"}, http.StatusOK)

	// 3. 审计读面：promote 必须留一行动作人 / 结论。
	status, raw := c.do(http.MethodGet, "/api/strategies/"+id+"/promotions?version_hash="+v1, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("promotions = %d (body: %s)", status, raw)
	}
	var events []map[string]any
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("decode promotions: %v (body: %s)", err, raw)
	}
	if len(events) != 1 || events[0]["actor"] != "qt32-live-e2e" ||
		events[0]["outcome"] != "promoted" || events[0]["artifact_hash"] != artV1 {
		t.Fatalf("promotion audit = %s", raw)
	}

	// 4. 闸门放行：pin 到 v1 与 latest 都要过，identity 用服务签发的两个 hash。
	for _, ref := range []string{id + "@" + v1, id} {
		identity, err := CheckStrategyRef(ctx, gate, ref)
		if err != nil {
			t.Fatalf("CheckStrategyRef(%q): %v", ref, err)
		}
		if identity == nil {
			t.Fatalf("CheckStrategyRef(%q) returned nil identity", ref)
		}
		if identity.StrategyID != id || identity.VersionHash != v1 ||
			identity.ArtifactHash != artV1 || identity.RefInput != ref {
			t.Fatalf("identity(%q) = %+v, want {id:%s version:%s artifact:%s}", ref, identity, id, v1, artV1)
		}
	}

	// 5. 闸门拒绝，两条真实路径都要 fail-closed：
	//    a) pin 到 v2 的**版本** hash：服务端在 resolve 阶段就答 404
	//       `version ... has no promoted artifact`（ErrNoActiveVersion），
	//       闸门映射为 404 / strategy_not_found——调用方拿不到任何回测结果。
	//    b) pin 到 v2 的 **artifact** hash：resolve 会如实回 status=validated，
	//       闸门在状态位处拒绝为 409 / strategy_not_promoted。
	//    两条合起来证明「不是先跑起来再看状态」，也覆盖了 409 这条主判据。
	_, err = CheckStrategyRef(ctx, gate, id+"@"+v2)
	if err == nil {
		t.Fatalf("CheckStrategyRef(%s@%s) passed for a validated-only version", id, v2)
	}
	if gateStatus, code := gateErrorCode(t, err); gateStatus != http.StatusNotFound || code != "strategy_not_found" {
		t.Fatalf("validated-only version = %d/%s, want 404/strategy_not_found", gateStatus, code)
	}

	_, err = CheckStrategyRef(ctx, gate, id+"@"+artV2)
	if err == nil {
		t.Fatalf("CheckStrategyRef(%s@%s) passed for a validated-only artifact", id, artV2)
	}
	if gateStatus, code := gateErrorCode(t, err); gateStatus != http.StatusConflict || code != "strategy_not_promoted" {
		t.Fatalf("validated-only artifact = %d/%s, want 409/strategy_not_promoted", gateStatus, code)
	}
}
