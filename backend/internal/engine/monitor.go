package engine

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/umbraforge/desktop/internal/procutil"
)

type MonitorSnapshot struct {
	CollectedAt string         `json:"collected_at"`
	HealthScore int            `json:"health_score"`
	Host        map[string]any `json:"host"`
	CPU         map[string]any `json:"cpu"`
	Memory      map[string]any `json:"memory"`
	Disk        map[string]any `json:"disk"`
	Network     map[string]any `json:"network"`
	Runtime     map[string]any `json:"runtime"`
}

func CollectMonitor(_ context.Context) (MonitorSnapshot, error) {
	now := time.Now()
	hi, _ := host.Info()
	loadAvg, _ := load.Avg()
	cpuPct, _ := cpu.Percent(300*time.Millisecond, false)
	perCore, _ := cpu.Percent(0, true)
	cpuInfo, _ := cpu.Info()
	counts, _ := cpu.Counts(true)
	vm, _ := mem.VirtualMemory()
	sw, _ := mem.SwapMemory()
	du, _ := disk.Usage("/")
	if runtime.GOOS == "windows" {
		du, _ = disk.Usage("C:")
	}
	io, _ := net.IOCounters(false)

	totalCPU := 0.0
	if len(cpuPct) > 0 {
		totalCPU = cpuPct[0]
	}
	cores := make([]float64, 0, len(perCore))
	cores = append(cores, perCore...)

	model := ""
	mhz := 0.0
	if len(cpuInfo) > 0 {
		model = cpuInfo[0].ModelName
		mhz = cpuInfo[0].Mhz
	}

	var recv, sent uint64
	if len(io) > 0 {
		recv = io[0].BytesRecv
		sent = io[0].BytesSent
	}

	memPct := 0.0
	if vm != nil {
		memPct = vm.UsedPercent
	}
	diskPct := 0.0
	if du != nil {
		diskPct = du.UsedPercent
	}

	score := 100
	score -= clampScore((totalCPU - 70) * 1.2)
	score -= clampScore((memPct - 75) * 1.5)
	score -= clampScore((diskPct - 85) * 2)
	if score < 1 {
		score = 1
	}
	if score > 100 {
		score = 100
	}

	hostMap := map[string]any{
		"hostname":         "",
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"platform":         "",
		"platform_version": "",
		"uptime_sec":       0,
		"procs":            0,
	}
	if hi != nil {
		hostMap["hostname"] = hi.Hostname
		hostMap["platform"] = hi.Platform
		hostMap["platform_version"] = hi.PlatformVersion
		hostMap["uptime_sec"] = hi.Uptime
		hostMap["procs"] = hi.Procs
		hostMap["kernel_arch"] = hi.KernelArch
	}

	loadMap := map[string]any{"load1": 0.0, "load5": 0.0, "load15": 0.0}
	if loadAvg != nil {
		loadMap["load1"] = loadAvg.Load1
		loadMap["load5"] = loadAvg.Load5
		loadMap["load15"] = loadAvg.Load15
	}

	memMap := map[string]any{}
	if vm != nil {
		memMap = map[string]any{
			"usage_percent": vm.UsedPercent,
			"total_mb":      float64(vm.Total) / 1024 / 1024,
			"used_mb":       float64(vm.Used) / 1024 / 1024,
			"available_mb":  float64(vm.Available) / 1024 / 1024,
			"free_mb":       float64(vm.Free) / 1024 / 1024,
			"cached_mb":     float64(vm.Cached) / 1024 / 1024,
		}
	}
	if sw != nil {
		memMap["swap_total_mb"] = float64(sw.Total) / 1024 / 1024
		memMap["swap_used_mb"] = float64(sw.Used) / 1024 / 1024
		memMap["swap_percent"] = sw.UsedPercent
	}

	diskMap := map[string]any{}
	if du != nil {
		diskMap = map[string]any{
			"path":          du.Path,
			"fstype":        du.Fstype,
			"usage_percent": du.UsedPercent,
			"total_gb":      float64(du.Total) / 1024 / 1024 / 1024,
			"used_gb":       float64(du.Used) / 1024 / 1024 / 1024,
			"free_gb":       float64(du.Free) / 1024 / 1024 / 1024,
		}
	}
	diskMap["disks"] = collectDiskList()
	memMap["slots"] = collectMemSlots()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	return MonitorSnapshot{
		CollectedAt: now.Format(time.RFC3339),
		HealthScore: score,
		Host:        hostMap,
		CPU: map[string]any{
			"usage_percent": totalCPU,
			"count_logical": counts,
			"model_name":    model,
			"mhz":           mhz,
			"per_core":      cores,
			"load1":         loadMap["load1"],
			"load5":         loadMap["load5"],
			"load15":        loadMap["load15"],
		},
		Memory: memMap,
		Disk:   diskMap,
		Network: map[string]any{
			"bytes_recv": recv,
			"bytes_sent": sent,
			"recv_gb":    float64(recv) / 1024 / 1024 / 1024,
			"sent_gb":    float64(sent) / 1024 / 1024 / 1024,
		},
		Runtime: map[string]any{
			"goos":          runtime.GOOS,
			"goarch":        runtime.GOARCH,
			"goroutines":    runtime.NumGoroutine(),
			"heap_alloc_mb": float64(ms.HeapAlloc) / 1024 / 1024,
			"sys_mb":        float64(ms.Sys) / 1024 / 1024,
		},
	}, nil
}

func clampScore(v float64) int {
	if v <= 0 {
		return 0
	}
	if v > 40 {
		return 40
	}
	return int(v)
}

// collectDiskList 返回所有本地磁盘/分区（不止主盘）。
// Windows 用 gopsutil 的盘符列表；macOS 只显示系统盘(APFS 根卷)，
// 其余 APFS 内部卷/外置/网络/Recovery/CoreSimulator 镜像不属于用户视角的磁盘；
// Linux 过滤 devtmpfs/proc/sysfs/tmpfs 等伪文件系统。
func collectDiskList() []map[string]any {
	parts, err := disk.Partitions(true)
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		if runtime.GOOS == "darwin" {
			if p.Mountpoint != "/" {
				continue
			}
		}
		if runtime.GOOS == "linux" && (p.Fstype == "devtmpfs" || p.Fstype == "proc" || p.Fstype == "sysfs" || p.Fstype == "tmpfs" && p.Mountpoint == "/dev/shm") {
			continue
		}
		u, uerr := disk.Usage(p.Mountpoint)
		if uerr != nil || u == nil || u.Total == 0 {
			continue
		}
		out = append(out, map[string]any{
			"path":          p.Mountpoint,
			"fstype":        p.Fstype,
			"usage_percent": u.UsedPercent,
			"total_gb":      float64(u.Total) / 1024 / 1024 / 1024,
			"used_gb":       float64(u.Used) / 1024 / 1024 / 1024,
			"free_gb":       float64(u.Free) / 1024 / 1024 / 1024,
		})
	}
	return out
}

// collectMemSlots 返回物理内存插槽明细（位置 + 容量）。
// Windows 读 SMBIOS（Win32_PhysicalMemory）；macOS/Linux 无通用免权限
// 途径，返回空数组（前端自动隐藏）。
func collectMemSlots() []map[string]any {
	if runtime.GOOS != "windows" {
		return []map[string]any{}
	}
	out, err := procutil.Command("powershell", "-NoProfile", "-NonInteractive",
		"-Command", "Get-CimInstance Win32_PhysicalMemory | ForEach-Object { \"$($_.DeviceLocator)|$($_.PartNumber)|$($_.Capacity)\" }").Output()
	if err != nil {
		return []map[string]any{}
	}
	slots := make([]map[string]any, 0, 4)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		capacity, cerr := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
		if cerr != nil || capacity == 0 {
			continue
		}
		slots = append(slots, map[string]any{
			"locator":     strings.TrimSpace(parts[0]),
			"part_number": strings.TrimSpace(parts[1]),
			"capacity_mb": float64(capacity) / 1024 / 1024,
			"capacity_gb": float64(capacity) / 1024 / 1024 / 1024,
		})
	}
	return slots
}
