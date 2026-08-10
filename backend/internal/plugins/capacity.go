package plugins

import (
	"context"
	"encoding/json"
	"github.com/umbraforge/desktop/internal/procutil"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// 打码槽的机器能力上限。
//
// 为什么必须钳制：打码是 CPU 密集的（一次 Turnstile 挑战 ≈ 1 核 × 20 秒），
// 槽数超过机器能力只会互相抢 CPU —— 单次解题变慢、超时变多，吞吐反而下降。
//
// 更要紧的是 Docker 模式下「机器」不是宿主：容器跑在 Docker VM 里
// （colima / Docker Desktop），VM 默认只分到 2 核 4G，跟宿主 12 核 32G 无关。
// 实测就是这个坑：宿主 12 核，容器只有 2 核 → auralith auto-tune 出 3 个 worker
// → 约 6 个/分钟；而 sub2api 在 8 核机器上是 12 个 worker → 30+/分钟。
// 差距不是功能缺失，而是容器只拿到了 2/12 的算力。
//
// 计算口径与三个打码引擎自身的 auto-tune 保持一致（见 veloraturn/src/autotune.go）：
//
//	按 CPU： cores × 1.5
//	按内存： (总内存 × 80%) / 400MB   —— 每个并发 Chromium ≈ 400MB
//	取两者较小值
const (
	// capacityMemPerWorkerMB 每个并发浏览器的内存占用估算（含渲染进程与 GPU 开销）。
	capacityMemPerWorkerMB = 400
	// capacityMemUsableRatio 只用 80% 内存，留给系统与打码器自身。
	capacityMemUsableRatio = 0.8
)

// capacityFromSpecs 按核数与内存算出可承载的打码槽数（与引擎 auto-tune 同口径）。
func capacityFromSpecs(cores, memMB int) int {
	if cores < 1 {
		cores = 1
	}
	byCPU := cores * 3 / 2
	if byCPU < 1 {
		byCPU = 1
	}
	w := byCPU
	if memMB > 0 {
		byRAM := int(float64(memMB)*capacityMemUsableRatio) / capacityMemPerWorkerMB
		if byRAM < 1 {
			byRAM = 1
		}
		if byRAM < w {
			w = byRAM
		}
	}
	return w
}

// dockerEngineSpecs 返回 Docker 引擎（容器真正能用的）核数与内存 MB。
// 取不到时返回 0,0，调用方回退宿主规格。
// 结果缓存 60 秒：dockerRunArgs 会被反复调用，每次都 shell 出去跑
// docker info（最长 6 秒）会把安装路径拖慢，而 VM 规格几乎不变。
func (c *Center) dockerEngineSpecs() (cores, memMB int) {
	c.specMu.Lock()
	if time.Since(c.specAt) < 60*time.Second && c.specCores > 0 {
		cores, memMB = c.specCores, c.specMemMB
		c.specMu.Unlock()
		return cores, memMB
	}
	c.specMu.Unlock()

	cores, memMB = c.probeDockerEngineSpecs()
	if cores > 0 {
		c.specMu.Lock()
		c.specCores, c.specMemMB, c.specAt = cores, memMB, time.Now()
		c.specMu.Unlock()
	}
	return cores, memMB
}

func (c *Center) probeDockerEngineSpecs() (cores, memMB int) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	out, err := procutil.CommandContext(ctx, dockerExecutable(), "info",
		"--format", "{{json .}}").Output()
	if err != nil {
		return 0, 0
	}
	var info struct {
		NCPU     int   `json:"NCPU"`
		MemTotal int64 `json:"MemTotal"`
	}
	if json.Unmarshal(out, &info) != nil {
		return 0, 0
	}
	return info.NCPU, int(info.MemTotal / (1024 * 1024))
}

// hostSpecs 宿主核数与内存 MB（内存取不到时返回 0，只按 CPU 算）。
func hostSpecs() (cores, memMB int) {
	cores = runtime.NumCPU()
	switch runtime.GOOS {
	case "darwin":
		if out, err := procutil.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			if b, e := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); e == nil {
				memMB = int(b / (1024 * 1024))
			}
		}
	case "linux":
		if out, err := procutil.Command("sh", "-c",
			`awk '/MemTotal/{print $2}' /proc/meminfo`).Output(); err == nil {
			if kb, e := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); e == nil {
				memMB = int(kb / 1024)
			}
		}
	case "windows":
		// 不用 wmic（Win11 起已移除），走 PowerShell CIM。
		out, err := procutil.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			"(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory").Output()
		if err == nil {
			if b, e := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); e == nil {
				memMB = int(b / (1024 * 1024))
			}
		}
	}
	return cores, memMB
}

// capacityWorkers 目标运行环境能承载的打码槽上限。
// Docker 模式看引擎规格（容器的真实配额），本机模式看宿主规格。
func (c *Center) capacityWorkers(id PluginID) int {
	if c.mode(id) == ModeDocker {
		if cores, memMB := c.dockerEngineSpecs(); cores > 0 {
			return capacityFromSpecs(cores, memMB)
		}
	}
	cores, memMB := hostSpecs()
	return capacityFromSpecs(cores, memMB)
}

// clampToCapacity 把期望槽数压到机器能力之内；desired<=0 表示交给引擎 auto-tune。
func (c *Center) clampToCapacity(id PluginID, desired int) int {
	if desired <= 0 {
		return 0
	}
	if cap := c.capacityWorkers(id); cap > 0 && desired > cap {
		return cap
	}
	return desired
}
