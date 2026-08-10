# UmbraForge 0.2.1 Review

## Round 1 · 配置与持久化
- JSON settings 路径：`~/Library/Application Support/UmbraForge/settings.json`
- SaveConfig 部分更新 merge 默认值，避免 zero-value 冲掉字段
- 验证：PUT config default_count=3 后读回含 ezsolver_url 默认

## Round 2 · 关键执行路径
- 本地 `grokregister.RunBatch` 启动/停止
- EDU list/upsert/delete/provision/callback
- 插件 install local/docker + ensure-docker + stop
- 验证：所有关键 API HTTP 200

## Round 3 · 行为语义
- 打码通道 5 种可选
- 邮箱 mailtm/edu/outlook
- 插件三引擎端口 8192/8193/8194

## Round 4 · 前后端/UI 契约
- 前端 axios `/api/v1/admin/*`
- SPA embed + NoRoute fallback
- 商务白 / 暗金 data-theme 切换持久化 localStorage

## Round 5 · 部署安装
- `/Applications/UmbraForge.app` 已安装
- codesign: Apple Development: 5892198@qq.com (U5RBWZSW73)
- 窗口：webview 内嵌 UI
- 图标：AppIcon.icns + icon.svg
