package engine

import (
	"testing"
)

// TestCountPendingImportsMatchesConvertCriteria 待入库计数必须与 ConvertToOAuth
// 的分流逻辑严格一致：AccessToken 为空 **且** SSO 非空。
//
// 两处若不一致，注册页会显示「待入库 5」而点进去只转换 3 个 —— 用户会以为
// 账号丢了。ConvertToOAuth 的实际分流是：
//   - AccessToken 非空 → 直接标记已入库（不触网），不算待入库
//   - SSO 为空        → Skipped，不算待入库
//   - 其余            → 进入转换队列，才是待入库
func TestCountPendingImportsMatchesConvertCriteria(t *testing.T) {
	list := []Account{
		{Email: "a@x.com", SSO: "sso-a", AccessToken: ""},      // 待入库
		{Email: "b@x.com", SSO: "sso-b", AccessToken: "tok-b"}, // 已有凭证
		{Email: "c@x.com", SSO: "", AccessToken: ""},           // 无 SSO，转换会 Skipped
		{Email: "d@x.com", SSO: "sso-d", AccessToken: ""},      // 待入库
		{Email: "e@x.com", SSO: "", AccessToken: "tok-e"},      // 已有凭证
		{Email: "f@x.com", SSO: "   ", AccessToken: "   "},     // 全空白：等同两者皆空
		{Email: "g@x.com", SSO: "sso-g", AccessToken: "   "},   // token 是空白 → 仍待入库
	}
	if got, want := CountPendingImports(list), 3; got != want {
		t.Errorf("CountPendingImports = %d, want %d", got, want)
	}
}

// TestCountPendingImportsEdgeCases 空列表/全已入库/全待入库三种边界。
func TestCountPendingImportsEdgeCases(t *testing.T) {
	if got := CountPendingImports(nil); got != 0 {
		t.Errorf("nil 列表应为 0，得到 %d", got)
	}
	if got := CountPendingImports([]Account{}); got != 0 {
		t.Errorf("空列表应为 0，得到 %d", got)
	}
	allDone := []Account{
		{SSO: "s1", AccessToken: "t1"},
		{SSO: "s2", AccessToken: "t2"},
	}
	if got := CountPendingImports(allDone); got != 0 {
		t.Errorf("全部已有凭证应为 0，得到 %d", got)
	}
	allPending := []Account{{SSO: "s1"}, {SSO: "s2"}, {SSO: "s3"}}
	if got := CountPendingImports(allPending); got != 3 {
		t.Errorf("全部待入库应为 3，得到 %d", got)
	}
}

// TestRecordAutoImportAccumulates 自动入库统计由多个 worker 的 onResult 并发写入，
// 必须逐次累加且成功/失败分项正确；ImportElapsedMS 是**累计**耗时而非墙钟
// （转换穿插在注册里做，墙钟等于整批耗时，说明不了入库成本）。
func TestRecordAutoImportAccumulates(t *testing.T) {
	e := &RegisterEngine{}
	e.recordAutoImport("a@x.com", true, "", 1200)
	e.recordAutoImport("b@x.com", false, "sso 已失效", 800)
	e.recordAutoImport("c@x.com", true, "", 1000)

	st := e.status
	if st.ImportDone != 3 {
		t.Errorf("ImportDone = %d, want 3", st.ImportDone)
	}
	if st.ImportSuccess != 2 {
		t.Errorf("ImportSuccess = %d, want 2", st.ImportSuccess)
	}
	if st.ImportFailed != 1 {
		t.Errorf("ImportFailed = %d, want 1", st.ImportFailed)
	}
	if st.ImportElapsedMS != 3000 {
		t.Errorf("ImportElapsedMS = %d, want 3000（累计耗时之和）", st.ImportElapsedMS)
	}
	if st.ImportLastEmail != "c@x.com" {
		t.Errorf("ImportLastEmail = %q, want c@x.com（最近一次）", st.ImportLastEmail)
	}
	// 最近一次是成功，必须清掉上一次的失败原因，否则界面会一直挂着旧错误
	if st.ImportLastError != "" {
		t.Errorf("ImportLastError = %q, want 空（最近一次成功应清空旧错误）", st.ImportLastError)
	}
}

// TestRecordAutoImportKeepsLastError 最近一次失败时要留下原因供界面显示。
func TestRecordAutoImportKeepsLastError(t *testing.T) {
	e := &RegisterEngine{}
	e.recordAutoImport("a@x.com", true, "", 500)
	e.recordAutoImport("b@x.com", false, "转换失败: 429 too many requests", 700)
	if e.status.ImportLastError == "" {
		t.Error("最近一次失败后 ImportLastError 不应为空")
	}
	if e.status.ImportLastEmail != "b@x.com" {
		t.Errorf("ImportLastEmail = %q, want b@x.com", e.status.ImportLastEmail)
	}
}
