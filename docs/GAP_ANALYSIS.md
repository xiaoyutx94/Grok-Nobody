# UmbraForge vs Sub2API Grok/EDU 差距分析

## 已同步（本轮）
- 代理池：多行 URL 持久化，启动注册自动注入 `proxy_urls`
- 账号管理：成功注册自动保存完整 email/password/sso/sso_rw/access_token/refresh_token
- 入库标记：批量 mark imported / import 接口
- 导出：json / sub2api(`sso_tokens`) / newapi(NDJSON) / full(`email----password----...`) / csv
- EDU：使用说明 + 官方超链、Token 权限表、加载 zones、单开/批开、手动 Worker、生成测试地址
- UI：商务白/暗金、对称 equal-card 双栏

## 仍弱于原版 / 可继续
- 未接 Sub2API 远端 CreateAccount 入库（桌面端为本地账号库语义）
- 无 Kiro remote engine / 历史排行榜 / Host HUD 系统监控全量
- 无 Yes/Next 打码密钥设置弹层的完整原版 settings tabs 全量搬运
- 无 pending-import 重试队列可视化（原版待转 OAuth 面板）
- EDU：CF zone 状态探针字段展示较简（依赖 ListZonesWithEmailStatus 返回）
- 无 iOS 同步与服务端多租户权限模型

## 建议下一轮优先级
1. SSO→OAuth 本地 convert（复用 xai 包）并写入 access/refresh
2. 可选推送到 Sub2API Admin API（CreateAccount）
3. 原版 Host HUD / 容量预设 / 历史任务
