package bootstrap

import (
	"strings"
	"testing"
)

func TestValidateServiceEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		envKey  string
		raw     string
		wantErr string // 空 = 期望通过
	}{
		{name: "empty", key: "data_service.url", envKey: "DATA_SERVICE_URL", raw: "", wantErr: "is empty"},
		{name: "no scheme", key: "data_service.url", envKey: "DATA_SERVICE_URL", raw: "data-service:8081", wantErr: "not an absolute URL"},
		{name: "no host", key: "data_service.url", envKey: "DATA_SERVICE_URL", raw: "http://", wantErr: "not an absolute URL"},
		{name: "localhost ok", key: "data_service.url", envKey: "DATA_SERVICE_URL", raw: "http://localhost:8081"},
		{name: "ipv4 ok", key: "data_service.url", envKey: "DATA_SERVICE_URL", raw: "http://127.0.0.1:8081"},
		{name: "unresolvable host", key: "data_service.url", envKey: "DATA_SERVICE_URL",
			raw: "http://data-service:8081", wantErr: "does not resolve here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateServiceEndpoint(tc.key, tc.envKey, tc.raw)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %q", tc.wantErr, err.Error())
			}
			// 报错必须点名 env，否则运维还是不知道该设置什么。
			if !strings.Contains(err.Error(), tc.envKey) {
				t.Fatalf("error should name env %s, got %q", tc.envKey, err.Error())
			}
		})
	}
}
