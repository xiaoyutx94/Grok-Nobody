# UmbraForge · 影铸

独立桌面端 **Grok 注册工作台**（从 Sub2API 完整复制注册 + EDU 邮箱能力，不修改原系统）。

## 代号
- **UmbraForge**（影铸）：暗影锻造 — 把账号注册、邮箱池、打码插件铸成一套本地工作台。
- 图标：`assets/icon.svg`

## 架构（与 Sub2API 一致）
- **前端**：Vue 3 + TypeScript + Vite + Tailwind
- **后端**：Go（内嵌 `grokregister` / `cfemail` 包 + 本地 JSON settings）
- **桌面壳**：当前提供原生 HTTP API + 前端；macOS 打包为 `.app` 并用开发者证书签名。Wails 可后续无缝包壳。
- **插件中心**：EzSolver / VeloraTurn / Auralith，支持 **local** 与 **docker** 安装模式。

## 目录
```
umbraforge/
  assets/icon.svg
  backend/          # Go module github.com/umbraforge/desktop
  frontend/         # Vue app
  plugins/          # ezsolver / veloraturn / auralith
  scripts/dev.sh
  scripts/build-macos.sh   # codesign: Apple Development: 5892198@qq.com
  scripts/build-windows.sh
  scripts/build-linux.sh
  bin/umbraforge
```

## 开发
```bash
./scripts/dev.sh
# API  http://127.0.0.1:17890
# UI   http://127.0.0.1:5179
```

仅后端：
```bash
cd backend && go run ./cmd/umbraforge -root ..
```

## 构建
```bash
./scripts/build-macos.sh
./scripts/build-linux.sh
./scripts/build-windows.sh    # Windows 主机：Garble + AES-256-GCM/Ed25519 + Inno Setup
```

Windows 发布脚本只生成受保护的 `exe + plugins.ufp`，不会再把三个打码核心的源码目录明文复制进安装包。安全边界、密钥管理和验证门禁见 [`docs/WINDOWS_PROTECTION.md`](docs/WINDOWS_PROTECTION.md)。公开发布仍需由发行方提供可信的 Authenticode 代码签名证书；仓库不会伪造生产证书。

## 插件
| 插件 | 端口 | local | docker |
|------|------|-------|--------|
| EzSolver | 8192 | python service.py | python:3.11-slim 挂载 |
| VeloraTurn | 8193 | go run / linux 二进制 | debian + 二进制 |
| Auralith | 8194 | auralithd | debian + 二进制 |

选择 Docker 时：`POST /api/v1/admin/plugins/ensure-docker` 会按 OS 尝试 brew/winget/get.docker.com。

## 数据目录
- macOS: `~/Library/Application Support/UmbraForge`
- Windows: `%APPDATA%/UmbraForge`
- Linux: `~/.config/umbraforge`

## 与 Sub2API 关系
- 原仓库功能**保留不动**
- 本目录独立 module，可单独发布
- 后续可增加「推送账号到 Sub2API」适配器
