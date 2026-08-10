package procutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedRawExec 允许继续用裸 exec.Command 的文件（附理由）。
// 这些都不会进 Windows 构建，或根本不在 Windows 上执行。
var allowedRawExec = map[string]string{
	// //go:build darwin —— 压根不编进 Windows 二进制
	"internal/plugins/window_nanny_darwin.go": "darwin-only build tag",
	// 无 build tag 会编进 Windows，但 WindowNanny.Start() 在
	// !windowNannySupported() 时直接 return，findSolverChromePIDs 永不执行；
	// 且 pgrep 在 Windows 上不存在，exec 会在创建进程前就报错，不会弹窗。
	"internal/plugins/window_nanny.go": "pgrep path unreachable on Windows (nanny disabled)",
}

// TestNoRawExecCommand 守住「Windows 不弹控制台窗口」这条不变式。
//
// 背景：Windows 上 exec.Command 启动控制台程序时，系统会为它分配一个 conhost
// 窗口。项目里有 100+ 处子进程调用，其中 Docker 状态/容器列表/WARP 健康检查
// 都是每几秒轮询一次的 —— 桌面上会持续闪黑框；打码引擎是长驻进程，它的窗口
// 会一直挂在任务栏。修法是全部改走 procutil.Command（构造时设
// CREATE_NO_WINDOW + HideWindow）。
//
// 这个测试防的是回归：新代码顺手写 exec.Command 就会让黑框重新出现，
// 而这种问题在 macOS 上开发时**完全看不到**，只有 Windows 用户会撞上。
func TestNoRawExecCommand(t *testing.T) {
	root := repoRoot(t)
	var violations []string

	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// 跳过 procutil 自身（它就是那层包装）与嵌入的前端产物
			base := info.Name()
			if base == "procutil" || base == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		if _, ok := allowedRawExec[filepath.ToSlash(rel)]; ok {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "exec.Command(") || strings.Contains(line, "exec.CommandContext(") {
				violations = append(violations,
					filepath.ToSlash(rel)+":"+itoa(i+1)+" "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历源码失败: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("发现 %d 处裸 exec.Command —— Windows 上每处都会弹出控制台窗口。\n"+
			"请改用 procutil.Command / procutil.CommandContext；\n"+
			"确实不进 Windows 构建的文件请加进 allowedRawExec 并写明理由。\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestAllowedRawExecFilesStillExist 白名单不能烂掉：文件改名/删除后必须同步清理，
// 否则白名单会静默失效，真正的违规被放过。
func TestAllowedRawExecFilesStillExist(t *testing.T) {
	root := repoRoot(t)
	for rel, reason := range allowedRawExec {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("白名单条目 %q（理由：%s）已不存在，请从 allowedRawExec 移除", rel, reason)
		}
	}
}

// repoRoot 从当前测试目录（internal/procutil）回溯到 backend 根。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}
	// internal/procutil → internal → backend
	return filepath.Dir(filepath.Dir(wd))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
