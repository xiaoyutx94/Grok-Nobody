//go:build windows

package plugins

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"golang.org/x/sys/windows/registry"
)

// findWindowsPython 在 PATH 之外探测 Windows 常见 Python 安装位置，
// 返回可执行文件绝对路径（不存在返回 ""）。探测顺序：
//  1. 注册表 HKCU/HKLM SOFTWARE\Python\PythonCore\<ver>\InstallPath
//  2. %LOCALAPPDATA%\Programs\Python\Python3x\python.exe（默认安装目录）
//  3. C:\Python3x\python.exe（传统目录）
//
// 版本选择：nodriver 与 3.14 有编码兼容问题，优先 3.11~3.13（取最高）；
// 只有 3.14 时也返回（venv 阶段会报兼容错误并提示换版本）。
func findWindowsPython() string {
	type cand struct {
		path string
		ver  int // 311 = 3.11
	}
	var cands []cand
	add := func(p string) {
		re := regexp.MustCompile(`Python(3)(\d+)`)
		if m := re.FindStringSubmatch(filepath.ToSlash(p)); len(m) == 3 {
			if v, err := strconv.Atoi(m[2]); err == nil {
				cands = append(cands, cand{path: p, ver: 300 + v})
				return
			}
		}
		cands = append(cands, cand{path: p})
	}
	// 注册表（HKCU 优先于 HKLM）
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `SOFTWARE\Python\PythonCore`, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		subs, _ := k.ReadSubKeyNames(-1)
		for _, sub := range subs {
			ik, err := registry.OpenKey(root, `SOFTWARE\Python\PythonCore\`+sub+`\InstallPath`, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			if dir, _, err := ik.GetStringValue(""); err == nil && dir != "" {
				add(filepath.Join(dir, "python.exe"))
			}
			_ = ik.Close()
		}
		_ = k.Close()
	}
	// 默认安装目录（python.org 安装器不带 Add to PATH 时的落点）
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		if ms, _ := filepath.Glob(filepath.Join(la, "Programs", "Python", "Python3*", "python.exe")); len(ms) > 0 {
			for _, m := range ms {
				add(m)
			}
		}
	}
	if ms, _ := filepath.Glob(`C:\Python3*\python.exe`); len(ms) > 0 {
		for _, m := range ms {
			add(m)
		}
	}
	if len(cands) == 0 {
		return ""
	}
	// 3.11~3.13 优先（取最高），否则取最高版本（可能是 3.14，留待 venv 报错提示）
	best := cands[0]
	bestScore := -1
	for _, c := range cands {
		score := 0
		if c.ver >= 311 && c.ver <= 313 {
			score = c.ver
		} else if c.ver > 0 {
			score = c.ver - 1000 // 3.14 等新版本排后面
		}
		if score > bestScore {
			best, bestScore = c, score
		}
	}
	return best.path
}
