# Grok 注册指纹实机基线（capture evidence）

本目录保存"真实浏览器 vs Go 注册客户端"的 on-wire 指纹实测证据，用于判定某个
`FingerprintProfile` 能否标记为 `verified`。**禁止手写/编造**：每条 fixture 必须来自
真机 capture，且记录 capture 日期、主机、浏览器版本、echo 端点。

## 采集方法

- Go 客户端：`go build ./cmd/grok-fingerprint-probe` 交叉编译到目标平台后在服务器执行

  ```bash
  ./grok-fp-probe --browser chrome --os linux \
    --echo-url https://tls.peet.ws/api/all --out go-chrome-linux.json
  ```

- 真实浏览器：在同一主机、同一出口用无头 Chromium 抓同一 echo 端点

  ```bash
  DISPLAY=:99 chromium-browser --headless=new --no-sandbox --disable-gpu \
    --user-data-dir=/tmp/cr --virtual-time-budget=15000 \
    --dump-dom https://tls.peet.ws/api/all
  ```

- fixture 入库前必须清洗：删除出口 `ip`、`donate`、`peetprint`（含机器熵）等字段，
  只保留 UA / JA3 / JA4 / JA4_r / akamai(H2) 等非敏感指纹项。

## 2026-07-19 首次基线（prod x64 主机，出口 api.p-box.online）

- 真机：`Chromium 150.0.7871.114 (snap, headless=new)` → `2026-07-19-chromium-150-linux-x64-real.json`
- Go 客户端：`azuretls chrome 模板，UA 声称 Chrome 150` → `2026-07-19-go-chrome-linux-x64-observed.json`
- EzSolver 节点：本机 `:8192` 与 ARM `151.145.76.6:8192` 均为 `sub2api-ezsolver/4`（协议 v4，未升 v5）；节点浏览器为 Chromium 150。

### 对比结论（决定 transport 与 profile 状态）

| 项 | 真机 Chromium 150 | Go(azuretls, 声称 150) | 结论 |
|----|-------------------|------------------------|------|
| JA4 前缀 | `t13d1516h2` | `t13d1516h2` | ✅ 一致 |
| 密码套件 | 15 项 | 同 | ✅ 一致 |
| TLS 扩展集合 | `0005,000a,...,ff01` | 同 | ✅ 一致 |
| ALPN | h2 | h2 | ✅ 一致 |
| HTTP/2 akamai | `1:65536;2:0;4:6291456;6:262144\|15663105\|0\|m,a,s,p` | 完全相同 | ✅ 一致 |
| 签名算法 | `0904,0905,0906,0403,0804,0401,0503,0805,0501,0806,0601` | `0403,0804,0401,0503,0805,0501,0806,0601` | ❌ 真机多 3 项（`0904/0905/0906`） |
| JA4 第三段 | `806a8c22fdea` | `d8a2da3f94cd` | ❌ 因签名算法不同 |

### 判定

- azuretls 现有 `chrome` 模板在**密码套件 / TLS 扩展 / ALPN / 全部 HTTP/2 参数**上与真实 Chrome 150 **一致**。
- 仅**签名算法列表**落后：真实 Chrome 150 在表头多出 `0904,0905,0906` 三个方案，azuretls 模板（约 Chrome 133 世代）没有。
- 因此：**声称 "Chrome 150" 的 profile 目前不能标 `verified`**——UA-major 与 TLS 签名算法不自洽（JA4 第三段可被交叉校验发现）。
- 两条可选修复路径（属产品/工程决策，见计划 §0.4）：
  1. **对齐声称版本**：让 recommended profile 声称 azuretls 模板真正对应的 Chrome major（约 133），换取"旧但完全自洽"。低成本、纯配置。
  2. **补齐签名算法**：为 Chrome-150 自定义 ClientHello（在 utls 层把 `0904/0905/0906` 加入 signature_algorithms），换取"当前且自洽"。需改传输层 + 重新 capture 验证，成本较高。

在任一路径完成并复测 JA4 第三段一致之前，Chrome-150 profile 维持 `experimental`。
