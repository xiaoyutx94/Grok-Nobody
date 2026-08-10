package server

import (
	"strings"
	"testing"

	"github.com/umbraforge/desktop/internal/engine"
	"github.com/umbraforge/desktop/internal/store"
)

// TestRouterRegistersAccountRoutes 固定住路由注册不 panic。
// gin 的 httprouter 对同层「静态段 + :wildcard」冲突会在注册时 panic，
// 新增的 /accounts/:id 与既有 /accounts/export、/accounts/clear 同层，必须实测。
func TestRouterRegistersAccountRoutes(t *testing.T) {
	st, err := store.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	api := &API{Accounts: engine.NewAccountService(st)}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("router registration panicked: %v", r)
		}
	}()
	r := api.Router()

	want := []string{
		"GET /api/v1/admin/grok-register/accounts",
		"GET /api/v1/admin/grok-register/accounts/:id",
		"POST /api/v1/admin/grok-register/accounts/:id/update",
		"POST /api/v1/admin/grok-register/accounts/:id/test",
		"POST /api/v1/admin/grok-register/accounts/:id/test-stream",
		"GET /api/v1/admin/grok-register/accounts/:id/models",
		"POST /api/v1/admin/grok-register/accounts/:id/verify",
		"POST /api/v1/admin/grok-register/accounts/:id/refresh-oauth",
		"POST /api/v1/admin/grok-register/accounts/delete",
		"POST /api/v1/admin/grok-register/accounts/clear",
		"GET /api/v1/admin/grok-register/accounts/export",
	}
	got := map[string]bool{}
	for _, ri := range r.Routes() {
		got[ri.Method+" "+ri.Path] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing route %q\nregistered:\n%s", w, strings.Join(keysOf(got), "\n"))
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
