package server

import (
	"encoding/json"
	"testing"
)

// markAbsentConcurrency 复刻 PUT /config 里「把缺失字段标成 -1」的那段逻辑，
// 以便在不起 HTTP 服务的情况下钉住契约。
func markAbsentConcurrency(raw []byte) (ez int, au int) {
	var cfg struct {
		EzSolverMaxConcur int `json:"ezsolver_max_concurrency"`
		AuralithMaxConcur int `json:"auralith_max_concurrency"`
	}
	_ = json.Unmarshal(raw, &cfg)
	ez, au = cfg.EzSolverMaxConcur, cfg.AuralithMaxConcur
	var present map[string]json.RawMessage
	if json.Unmarshal(raw, &present) == nil {
		if _, ok := present["ezsolver_max_concurrency"]; !ok {
			ez = -1
		}
		if _, ok := present["auralith_max_concurrency"]; !ok {
			au = -1
		}
	}
	return ez, au
}

// TestPartialConfigUpdateKeepsConcurrencyCeiling 打码并发的 0 是合法值
// （=跟随注册并发），所以不能再用 0 判断「字段没传」。
// 否则任何只改别的字段的局部更新，都会把用户设的上限悄悄清成 auto——
// 实测就发生过：PUT {"default_concurrency":8} 把 ezsolver 上限 4 冲成了 0。
func TestPartialConfigUpdateKeepsConcurrencyCeiling(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		wantEz int
		wantAu int
	}{
		{
			name:   "只改并发，两个上限都应标为未提供",
			body:   `{"default_concurrency":8}`,
			wantEz: -1, wantAu: -1,
		},
		{
			name:   "显式传 0 表示 auto，必须保留 0（不能变 -1）",
			body:   `{"ezsolver_max_concurrency":0,"auralith_max_concurrency":0}`,
			wantEz: 0, wantAu: 0,
		},
		{
			name:   "显式传值原样透传",
			body:   `{"ezsolver_max_concurrency":4,"auralith_max_concurrency":6}`,
			wantEz: 4, wantAu: 6,
		},
		{
			name:   "只传其中一个，另一个标为未提供",
			body:   `{"ezsolver_max_concurrency":4}`,
			wantEz: 4, wantAu: -1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ez, au := markAbsentConcurrency([]byte(tc.body))
			if ez != tc.wantEz {
				t.Errorf("ezsolver: got %d want %d", ez, tc.wantEz)
			}
			if au != tc.wantAu {
				t.Errorf("auralith: got %d want %d", au, tc.wantAu)
			}
		})
	}
}
