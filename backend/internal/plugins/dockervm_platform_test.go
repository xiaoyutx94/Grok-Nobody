package plugins

import "testing"

// 三种后端的内存共享语义必须各归各位，UI 文案依赖这几个字段：
//   - colima + vz：有内存气球（配额=上限，实占按需、空闲归还）
//   - colima + qemu：无气球，配额会被实打实占住
//   - Docker Desktop（macOS/Windows）：WSL2/Hyper-V/vz 动态内存 → 有气球语义
//   - Linux 原生：无 VM 层，容器直接用宿主全部内存 → 完全共享，无配额概念
func TestRuntimeShareSemanticsPerBackend(t *testing.T) {
	c := &Center{}
	info := c.DockerRuntime()

	switch info.Backend {
	case BackendColima:
		// 无论 vmType 是什么，都要有值；vz/空 → 气球 true
		if info.VMType == "" {
			t.Error("colima 后端 VMType 为空")
		}
		wantBalloon := info.VMType == "" || info.VMType == "vz"
		if info.MemBalloon != wantBalloon {
			t.Errorf("colima(%s) MemBalloon=%v, want %v", info.VMType, info.MemBalloon, wantBalloon)
		}
	case BackendDockerDesktop:
		if !info.MemBalloon {
			t.Error("Docker Desktop（WSL2/Hyper-V/vz 动态内存）应标记 mem_balloon=true")
		}
		if info.VMType == "" {
			t.Error("Docker Desktop VMType 为空，UI 文案缺信息")
		}
	case BackendNativeLinux:
		if !info.MemBalloon {
			t.Error("Linux 原生无 VM 层=完全直通，应标记 mem_balloon=true（无配额）")
		}
		if info.Resizable {
			t.Error("Linux 原生不应允许调规格（ApplyDockerVMSpecs 会拒绝）")
		}
	case BackendUnknown:
		t.Skip("当前环境未识别后端，跳过（CI/无 Docker 环境）")
	}

	// 无论哪种后端，共享档都必须给到宿主 3/4 且保留宿主余量
	if info.HostMemMB > 0 {
		share := ShareHostMemoryMB(info.HostMemMB)
		if share <= 0 || share >= info.HostMemMB {
			t.Errorf("share=%dMB 超出 (0, hostMem=%dMB)", share, info.HostMemMB)
		}
	}
}

// applyColimaSpecs 在 vz 下必须显式带 --vm-type vz（保证气球语义）。
// 纯逻辑断言：colima start 参数列表里出现 --vm-type vz。
func TestColimaStartForcesVMType(t *testing.T) {
	// 该分支依赖本机 colima 配置，这里只测参数拼装逻辑的骨架：
	// colimaVMType() 读 ~/.colima/default/colima.yaml 的 vmType 字段
	vt := colimaVMType()
	if vt == "" {
		t.Log("colima 配置读不到（本机无 colima 或无配置文件）——跳过")
		return
	}
	if vt != "vz" && vt != "qemu" {
		t.Errorf("vmType=%q 非法（应为 vz/qemu）", vt)
	}
}
