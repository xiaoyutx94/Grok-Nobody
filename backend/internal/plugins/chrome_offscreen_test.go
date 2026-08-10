package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOffscreenEnabledTriState(t *testing.T) {
	orig, had := os.LookupEnv("UMBRAFORGE_CAPTCHA_OFFSCREEN")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", orig)
		} else {
			_ = os.Unsetenv("UMBRAFORGE_CAPTCHA_OFFSCREEN")
		}
	})

	for _, v := range []string{"1", "true", "YES"} {
		_ = os.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", v)
		if !offscreenEnabled() {
			t.Errorf("%q should enable", v)
		}
	}
	for _, v := range []string{"0", "false", "No"} {
		_ = os.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", v)
		if offscreenEnabled() {
			t.Errorf("%q should disable", v)
		}
	}
	_ = os.Unsetenv("UMBRAFORGE_CAPTCHA_OFFSCREEN")
	want := runtime.GOOS != "linux"
	if offscreenEnabled() != want {
		t.Errorf("default on %s = %v, want %v (Linux 有 Xvfb 不需要)", runtime.GOOS, offscreenEnabled(), want)
	}
}

func TestUpsertEnvReplacesRatherThanDuplicates(t *testing.T) {
	env := []string{"A=1", "CHROME_PATH=/old/chrome", "B=2"}
	out := upsertEnv(env, "CHROME_PATH", "/new/chrome")
	n := 0
	for _, kv := range out {
		if strings.HasPrefix(kv, "CHROME_PATH=") {
			n++
			if kv != "CHROME_PATH=/new/chrome" {
				t.Errorf("value = %q", kv)
			}
		}
	}
	if n != 1 {
		t.Errorf("CHROME_PATH appears %d times, want exactly 1", n)
	}
	// 其他变量保持
	for _, want := range []string{"A=1", "B=2"} {
		found := false
		for _, kv := range out {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Errorf("lost %q", want)
		}
	}
	// 新增键
	out = upsertEnv([]string{"A=1"}, "NEW", "v")
	if out[len(out)-1] != "NEW=v" {
		t.Errorf("append failed: %v", out)
	}
}

func TestShellQuoteHandlesSpacesAndQuotes(t *testing.T) {
	// macOS Chrome 路径带空格，不引会被拆成两个参数
	got := shellQuote("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	if got != "'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'" {
		t.Errorf("got %s", got)
	}
	if q := shellQuote("/tmp/it's/chrome"); q != `'/tmp/it'\''s/chrome'` {
		t.Errorf("single quote not escaped: %s", q)
	}
}

// TestWrapperScriptAppendsOffscreenFlagsAndPreservesArgs 真跑包装脚本：
// 用假的 "chrome"（打印收到的参数）验证脚本把原参数原样透传并追加离屏旗标。
func TestWrapperScriptAppendsOffscreenFlagsAndPreservesArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wrapper is sh-based")
	}
	root := t.TempDir()

	// 假 Chrome：把每个参数单独一行打印出来
	fakeChrome := filepath.Join(root, "fake chrome") // 故意带空格
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done\n"
	if err := os.WriteFile(fakeChrome, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_PATH", fakeChrome)
	t.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", "1")

	c := &Center{root: root}
	wrapper := c.ensureChromeOffscreenWrapper()
	if wrapper == "" {
		t.Fatal("wrapper not generated")
	}
	info, err := os.Stat(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("wrapper not executable: %v", info.Mode())
	}

	out, err := exec.Command(wrapper, "--user-data-dir=/tmp/p one", "--no-sandbox").CombinedOutput()
	if err != nil {
		t.Fatalf("run wrapper: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	// 原参数必须原样保留（含空格的那个不能被拆开）
	if len(lines) < 4 {
		t.Fatalf("expected >=4 args, got %v", lines)
	}
	if lines[0] != "--user-data-dir=/tmp/p one" {
		t.Errorf("arg with space mangled: %q", lines[0])
	}
	if lines[1] != "--no-sandbox" {
		t.Errorf("arg 2 = %q", lines[1])
	}
	// 离屏旗标追加在后面（后出现的同名旗标胜出，覆盖调用方自带的 window-position）
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "--window-position=-32000,-32000") {
		t.Errorf("missing offscreen position: %v", lines)
	}
	if !strings.Contains(joined, "--window-size=1280,800") {
		t.Errorf("missing window size: %v", lines)
	}
	// 绝不能引入 headless（会破坏 Turnstile 通过率）
	if strings.Contains(joined, "--headless") {
		t.Errorf("wrapper must not add headless: %v", lines)
	}
}

func TestWrapperOverridesCallerWindowPosition(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wrapper is sh-based")
	}
	root := t.TempDir()
	fakeChrome := filepath.Join(root, "chrome")
	if err := os.WriteFile(fakeChrome, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_PATH", fakeChrome)
	t.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", "1")

	c := &Center{root: root}
	wrapper := c.ensureChromeOffscreenWrapper()
	out, err := exec.Command(wrapper, "--window-position=0,0").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	s := string(out)
	// 调用方的 0,0 在前，离屏在后 → Chrome 取后者
	if strings.Index(s, "--window-position=0,0") > strings.Index(s, "--window-position=-32000") {
		t.Errorf("offscreen flag must come after caller's: %s", s)
	}
}

func TestApplyCaptchaOffscreenEnv(t *testing.T) {
	root := t.TempDir()
	fakeChrome := filepath.Join(root, "chrome")
	if err := os.WriteFile(fakeChrome, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Center{root: root}

	t.Run("enabled", func(t *testing.T) {
		t.Setenv("CHROME_PATH", fakeChrome)
		t.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", "1")
		env := c.applyCaptchaOffscreenEnv([]string{"PORT=8192"})
		joined := strings.Join(env, "\n")
		if !strings.Contains(joined, "EZSOLVER_OFFSCREEN=1") {
			t.Error("EZSOLVER_OFFSCREEN=1 not set (EzSolver/VeloraTurn 自加旗标靠它)")
		}
		if runtime.GOOS != "windows" && !strings.Contains(joined, "CHROME_PATH="+filepath.Join(root, "plugins", ".runtime")) {
			t.Errorf("CHROME_PATH not pointed at wrapper: %v", env)
		}
		if !strings.Contains(joined, "PORT=8192") {
			t.Error("caller env dropped")
		}
	})

	t.Run("disabled passes explicit off", func(t *testing.T) {
		t.Setenv("UMBRAFORGE_CAPTCHA_OFFSCREEN", "0")
		env := c.applyCaptchaOffscreenEnv([]string{"PORT=8192"})
		joined := strings.Join(env, "\n")
		if !strings.Contains(joined, "EZSOLVER_OFFSCREEN=0") {
			t.Error("must pass explicit 0 so solvers don't re-enable by platform default")
		}
	})
}
