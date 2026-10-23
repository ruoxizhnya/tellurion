package main

import (
	"encoding/json"
	"strings"
	"testing"

	"tellurion/pkg/sync"
)

// DATA-14：零 DB 依赖的集合端点契约钉子。
// 空结果经 encoding/json 序列化后必须是 []，不能是 null —— 这里走的是
// handler 实际使用的 collectionPayload，而不是另写一份等价逻辑。
func TestCollectionPayload_EmptyResultSerializesAsArray(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		payload map[string]any
	}{
		{"jobs", "jobs", collectionPayload[*sync.Job]("jobs", nil)},
		{"schedules", "schedules", collectionPayload[any]("schedules", nil)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal %s payload: %v", tc.name, err)
			}
			got := string(raw)
			if !strings.Contains(got, `"`+tc.key+`":[]`) {
				t.Fatalf("empty %s must serialize as [], got %s", tc.key, got)
			}
			if strings.Contains(got, `"`+tc.key+`":null`) {
				t.Fatalf("empty %s must not serialize as null, got %s", tc.key, got)
			}
		})
	}
}
