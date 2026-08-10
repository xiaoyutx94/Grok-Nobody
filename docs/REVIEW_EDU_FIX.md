# UmbraForge EDU 深度修复 Review（2026-07-24）

## 问题
1. Zone 列表无法判断 CF Email Routing 是否已开通（只比对本地池）
2. 无法选择「CF 已开通」域名加入 EDU 池
3. Outlook 注册花架子；EDU 无 Worker 多选；缺 use_edu_subdomain

## Round 1 · 配置与持久化
- `EduWorker.use_edu_subdomain` 持久化
- 新建 Worker 默认 `enabled=true`
- `SelectByIDs` 支持按 id 过滤启用 Worker
- 验证：结构体 JSON 字段存在；List/Upsert 编译通过

## Round 2 · 关键执行路径
- zones 默认 `probe_email_routing=true`（兼容 `probe`）
- 开通/批量开通成功后刷新 zones 状态
- 批量跳过已在池；「选已开通未入池」+「开通/入池所选」
- 注册 Start 同步校验 EDU 空池 / Outlook 空数据
- 验证：`go build`；headless `/health` 200；zones 非法 token 返回 CF 错误（探测链路通）

## Round 3 · 行为语义
- 状态四态：已在池 / CF已开通 / 状态未知 / 未开通
- 未知不计入「未开通」筛选
- Outlook：`ParseOutlookData` 真正注入 EmailServices
- EDU：`cf_worker_ids` 多选；`SetUseEduSubdomain`

## Round 4 · 前后端契约
- listZones timeout 180s；provision 300s；batch 600s
- generate-addresses 支持 `use_edu_subdomain`
- start body `cf_worker_ids`；错误 `400 {error}`

## Round 5 · 部署 / 缓存
- 前端 build + embed dist rsync
- `build-macos.sh` 增加 embed 同步
- 签名安装 `/Applications/UmbraForge.app`
- 请**完全退出并重启**桌面 App 以加载新二进制

## 使用方式
1. EDU 页填 CF 凭证 →「加载并探测开通状态」
2. 点「选已开通未入池」→「开通/入池所选」
3. 注册页邮箱模式选 edu → 勾选 Worker → 开始
