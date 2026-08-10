//go:build !windows

package plugins

// findWindowsPython 仅 Windows 有意义（注册表 + 默认安装目录探测），
// 其他平台返回空串，由 PATH 查找（python3/python）兜底。
func findWindowsPython() string { return "" }
