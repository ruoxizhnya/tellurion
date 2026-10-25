package marketdata

// 裁-15：data 服务上了鉴权闸后，core 作为服务间调用方必须带 Bearer。
// 这里只钉「头有没有发」，不问 data 侧怎么校验（那在 tellurion-data 的
// policy 测试里）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

func TestHTTPProvider_SendsServiceToken(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	p := NewHTTPProviderWithToken(ts.URL, " svc-token ", zerolog.Nop())
	if err := p.CheckConnectivity(context.Background()); err != nil {
		t.Fatalf("CheckConnectivity: %v", err)
	}
	if got != "Bearer svc-token" {
		t.Fatalf("want Bearer svc-token, got %q", got)
	}
}

func TestHTTPProvider_OmitsTokenWhenEmpty(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	p := NewHTTPProvider(ts.URL, zerolog.Nop())
	if err := p.CheckConnectivity(context.Background()); err != nil {
		t.Fatalf("CheckConnectivity: %v", err)
	}
	if got != "" {
		t.Fatalf("empty token must not send Authorization, got %q", got)
	}
}
