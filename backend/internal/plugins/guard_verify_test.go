package plugins

import "testing"

// TestGuardVerifyUserContainers 独立复核「用户其它项目的容器不会被删」这条承诺。
// 用户机器上真实跑着 red-postgres / red-redis（数据库数据，删了不可逆），
// 所以这条保证必须能被单独验证，而不是只靠一句口头结论。
func TestGuardVerifyUserContainers(t *testing.T) {
	c := NewCenter(t.TempDir())
	for _, name := range []string{"red-postgres", "red-redis"} {
		managed := isManagedContainer(name)
		err := c.ContainerAction(name, ActionRemove)
		t.Logf("GUARDCHECK name=%s managed=%v remove_rejected=%v", name, managed, err != nil)
		if managed {
			t.Errorf("%s 被判为托管容器 —— 会被允许删除", name)
		}
		if err == nil {
			t.Errorf("%s 的删除请求没有被拒绝", name)
		}
	}
	// 反向：自己的容器必须允许删（否则功能形同废掉）
	for _, name := range []string{"umbraforge-auralith", "umbra-warp-41000"} {
		if !isManagedContainer(name) {
			t.Errorf("%s 应被判为托管容器", name)
		}
		t.Logf("GUARDCHECK name=%s managed=true (可删)", name)
	}
}
