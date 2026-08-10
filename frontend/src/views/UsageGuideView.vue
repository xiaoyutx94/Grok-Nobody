<script setup lang="ts">
// 使用说明 —— 商业化文档站布局：Hero + 左侧 Sticky 目录 + 分章节功能明细。
// 图标统一用扁平线条 SVG（SF Symbols 风格，与侧边栏一致），不依赖系统 emoji 字体。
const S = (inner: string, size = 15) =>
  `<svg class="gico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:${size}px;height:${size}px">${inner}</svg>`

const sections = [
  { id: 'quickstart', label: '快速上手', icon: S('<circle cx="12" cy="12" r="9"/><path d="M10 8.5v7l5.5-3.5z"/>') },
  { id: 'home', label: '首页监控', icon: S('<path d="M3 10.5 12 3l9 7.5"/><path d="M5 9.5V21h14V9.5"/>') },
  { id: 'register', label: 'Grok 注册', icon: S('<path d="M13 2 4.5 13.5H11L10 22l8.5-11.5H13z"/>') },
  { id: 'accounts', label: '账号管理', icon: S('<circle cx="12" cy="8" r="4"/><path d="M4 21c0-4 3.6-6.5 8-6.5s8 2.5 8 6.5"/>') },
  { id: 'proxy', label: '代理池', icon: S('<path d="M11 5 7 9l4 4"/><path d="M7 9h10a4 4 0 0 1 0 8"/><path d="M13 19l4-4-4-4"/><path d="M17 15H7a4 4 0 0 1 0-8"/>') },
  { id: 'warp', label: 'WARP 代理', icon: S('<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.5 2.6 3.6 5.6 3.6 9s-1.1 6.4-3.6 9c-2.5-2.6-3.6-5.6-3.6-9s1.1-6.4 3.6-9"/>') },
  { id: 'edu', label: 'EDU 邮箱', icon: S('<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>') },
  { id: 'icloud', label: 'iCloud 邮箱', icon: S('<path d="M7 16a4 4 0 1 1 .5-7.96A5.5 5.5 0 0 1 18 9.5 3.5 3.5 0 0 1 17.5 16H7z"/><path d="M12 16v-5"/><path d="M9.5 13.5 12 11l2.5 2.5"/>') },
  { id: 'plugins', label: '插件中心 / 打码', icon: S('<rect x="4" y="4" width="6" height="6" rx="1.5"/><rect x="14" y="4" width="6" height="6" rx="1.5"/><rect x="4" y="14" width="6" height="6" rx="1.5"/><rect x="14" y="14" width="6" height="6" rx="1.5"/>') },
  { id: 'docker', label: 'Docker 管理', icon: S('<rect x="3" y="11" width="18" height="7" rx="1.5"/><path d="M7 11V8M11 11V6M15 11V8"/><path d="M3 18c2.5 2 6 2 9 0"/>') },
  { id: 'settings', label: '设置 / 打码通道', icon: S('<circle cx="12" cy="12" r="3"/><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5 5l2 2M17 17l2 2M19 5l-2 2M7 17l-2 2"/>') },
  { id: 'faq', label: '常见问题', icon: S('<circle cx="12" cy="12" r="9"/><path d="M9.3 9.2a2.8 2.8 0 0 1 5.4 1c0 1.8-2.7 2.2-2.7 3.8"/><path d="M12 17.4h.01"/>') },
]

// 章节标题里的图标（按 section id 取，保持与目录/入口一致）
function secIcon(id: string): string {
  return sections.find((s) => s.id === id)?.icon || ''
}

function scrollTo(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
</script>

<template>
  <div class="guide">
    <!-- Hero -->
    <div class="hero">
      <div class="hero-inner">
        <span class="hero-badge">PRODUCT GUIDE · v1.0</span>
        <h1>Grok-Nobody · 使用说明</h1>
        <p class="hero-sub">Grok 账号注册工作台 —— 从代理、邮箱、打码到账号入库的完整自动化流水线。<br />本指南覆盖全部功能模块，按章节明细到每个操作。</p>
        <div class="hero-chips">
          <button v-for="s in sections.slice(0, 6)" :key="s.id" class="chip" @click="scrollTo(s.id)">
            <span v-html="s.icon"></span>{{ s.label }}
          </button>
        </div>
      </div>
    </div>

    <div class="doc-layout">
      <!-- Sticky 目录 -->
      <aside class="toc">
        <div class="toc-title">本页目录</div>
        <a v-for="s in sections" :key="s.id" class="toc-item" :href="'#' + s.id" @click.prevent="scrollTo(s.id)">
          <span class="toc-ico" v-html="s.icon"></span>{{ s.label }}
        </a>
        <div class="toc-note">提示：所有操作均实时生效并持久化保存，无需手动「保存」之外的操作。</div>
      </aside>

      <!-- 内容 -->
      <main class="docs">
        <!-- 快速上手 -->
        <section id="quickstart" class="sec">
          <span class="kicker">QUICKSTART</span>
          <h2><span class="h-ico" v-html="secIcon('quickstart')"></span>快速上手</h2>
          <p class="lead">三分钟跑通「第一个 Grok 账号」：引擎 → 代理 → 注册。</p>
          <div class="steps">
            <div class="step"><div class="step-no">1</div><div><b>准备打码引擎</b><p>进入「插件中心」，将 <em>EzSolver / VeloraTurn / Auralith</em> 安装为本地模式（Windows 自动使用本机 Chrome；无 Docker 也可运行）。健康状态显示 <b>healthy</b> 即就绪。</p></div></div>
            <div class="step"><div class="step-no">2</div><div><b>导入代理池</b><p>「代理池」页按 <code>scheme://user:pass@host:port</code> 格式粘贴代理（支持 socks5/http），点「批量导入」。注册默认随机挑选，失败的代理自动冷却。</p></div></div>
            <div class="step"><div class="step-no">3</div><div><b>开始注册</b><p>「Grok 注册」页填写数量（如 50），邮箱模式选 <em>Mail.tm</em> 或 <em>EDU 域名池</em>，打码通道选 <em>VeloraTurn</em>（Windows 最快），点「开始注册」。任务自动并发、自动暂停、自动入库。</p></div></div>
          </div>
          <div class="callout"><span class="h-ico" v-html="S('<path d=\'M9 18h6M10 21h4\'/><path d=\'M12 3a6 6 0 0 1 3.5 10.9c-.8.6-1.5 1.3-1.5 2.1h-4c0-.8-.7-1.5-1.5-2.1A6 6 0 0 1 12 3z\'/>', 13)"></span>注册页面右上角可切换 <b>商务白 / 暗金</b> 两套主题，偏好自动记忆。</div>
        </section>

        <!-- 首页监控 -->
        <section id="home" class="sec">
          <span class="kicker">OVERVIEW</span>
          <h2><span class="h-ico" v-html="secIcon('home')"></span>首页监控</h2>
          <p class="lead">工作台总览：一眼看清系统健康状况与打码容量。</p>
          <table class="tbl">
            <thead><tr><th>卡片 / 区域</th><th>功能明细</th></tr></thead>
            <tbody>
              <tr><td><b>Docker 运行状态</b></td><td>检测 Docker 守护进程 / 虚拟化前置（WSL2、Hyper-V），显示 CPU 核数与内存、当前打码槽位数。前置不满足时给出精确修复指引（如「需重启系统」）。</td></tr>
              <tr><td><b>打码引擎健康</b></td><td>三款引擎（EzSolver / VeloraTurn / Auralith）的本地 / 容器运行状态与健康检查结果。</td></tr>
              <tr><td><b>注册任务状态</b></td><td>最近一次注册任务的进度、成功率、入库数量与耗时。</td></tr>
              <tr><td><b>代理池概览</b></td><td>可用代理数量与格式统计。</td></tr>
              <tr><td><b>快速入口</b></td><td>一键跳转：开始注册、导入代理、安装打码、部署 Docker 容器。</td></tr>
            </tbody>
          </table>
        </section>

        <!-- Grok 注册 -->
        <section id="register" class="sec">
          <span class="kicker">CORE</span>
          <h2><span class="h-ico" v-html="secIcon('register')"></span>Grok 注册</h2>
          <p class="lead">批量注册核心页。所有参数既可页面填写，也可作为批量任务配置。</p>
          <h3>任务参数明细</h3>
          <table class="tbl">
            <thead><tr><th>参数</th><th>说明</th><th>建议值</th></tr></thead>
            <tbody>
              <tr><td><code>注册数量</code></td><td>本次任务要注册的账号总数。</td><td>50–200</td></tr>
              <tr><td><code>并发数</code></td><td>同时进行的注册线程数。过高会放大邮箱/代理限流，过低则吞吐不足。</td><td>3–8</td></tr>
              <tr><td><code>任务间隔 / 步骤间隔</code></td><td>任务间与注册步骤间的延时（秒），用于降低风控节奏。</td><td>0–3</td></tr>
              <tr><td><code>邮箱模式</code></td><td><b>Mail.tm</b> 临时邮箱（免配置）· <b>EDU 域名池</b>（自有 CF 域名，可多选）· <b>iCloud 取码平台</b>（自动取号+轮询验证码）· <b>Outlook 导入</b>（自备凭证批量粘贴）。</td><td>按资源选择</td></tr>
              <tr><td><code>打码通道</code></td><td>captcha provider：<b>VeloraTurn</b>（Windows 本机最快 3.2s）· <b>Auralith</b>（Linux/容器最快 5.5s）· <b>EzSolver</b>（Python，兼容性最好）· 三方服务（YesCaptcha / NextCaptcha）。</td><td>Windows→vt，Linux→au</td></tr>
              <tr><td><code>系统类型</code></td><td>模拟注册设备系统（macOS / Windows），影响浏览器 UA 指纹。</td><td>macos</td></tr>
              <tr><td><code>浏览器</code></td><td>chrome / chromium。</td><td>chrome</td></tr>
              <tr><td><code>跳过邮箱验证</code></td><td>勾选后注册流程不等待验证码（用于调试链路，账号不可用）。</td><td>默认关</td></tr>
              <tr><td><code>代理来源</code></td><td>注册走「代理池随机」或指定单条代理；打码可独立选择 <b>跟随注册代理</b> 或直连（推荐跟随注册代理，保证 IP 一致）。</td><td>代理池 + 跟随</td></tr>
            </tbody>
          </table>
          <h3>自动化与容错</h3>
          <ul class="lst">
            <li><b>自动暂停</b>：连续失败超过阈值（默认 10）自动暂停 5 分钟，避免代理/邮箱全挂时空转。</li>
            <li><b>代理失败冷却</b>：单个代理连续失败 3 次自动冷却 10 分钟，成功自动解除。</li>
            <li><b>自动入库</b>（设置页开启 <code>auto_import</code>）：注册成功的账号自动写入账号库（邮箱+密码+SSO 凭证），无需手动导出。</li>
            <li><b>自动导出</b>（<code>auto_export</code>）：成功后按配置格式自动导出到文件。</li>
            <li><b>注册日志</b>：任务过程实时滚动，失败原因逐条可查（邮箱创建失败 / 打码超时 / 页面变化等）。</li>
          </ul>
          <div class="callout"><span class="h-ico" v-html="S('<path d=\'M12 3 2.5 19.5h19z\'/><path d=\'M12 10v4\'/><path d=\'M12 17.2h.01\'/>', 13)"></span>成功率优化：若「邮箱创建失败」占比高，说明临时邮箱服务被限流 —— 换 EDU 域名池或降低并发；若「打码失败」占比高，先测打码引擎健康与代理连通。</div>
        </section>

        <!-- 账号管理 -->
        <section id="accounts" class="sec">
          <span class="kicker">ASSETS</span>
          <h2><span class="h-ico" v-html="secIcon('accounts')"></span>账号管理</h2>
          <p class="lead">注册成功与导入的账号统一管理，支持单账号全操作。</p>
          <table class="tbl">
            <thead><tr><th>功能</th><th>明细</th></tr></thead>
            <tbody>
              <tr><td><b>账号列表</b></td><td>邮箱、状态、最近测试结果、代理绑定一屏总览；搜索 / 筛选 / 分页。</td></tr>
              <tr><td><b>一键复制</b></td><td>邮箱与密码点一下即复制。</td></tr>
              <tr><td><b>测试对话</b></td><td>用账号 OAuth 凭证真实调用 x.ai 对话（带官方 Grok CLI 指纹：UA <code>grok-shell/…</code> + <code>x-xai-token-auth</code> + <code>x-grok-client-version</code>），流式直播回复，验证账号可用性。</td></tr>
              <tr><td><b>校验凭证</b></td><td>重新验证 SSO/OAuth 凭证有效性（过期自动标记）。</td></tr>
              <tr><td><b>重取凭证</b></td><td>登录态失效时重新登录 x.ai 获取新凭证。</td></tr>
              <tr><td><b>更新 / 删除</b></td><td>修改邮箱/密码/备注；删除账号（含批量）。</td></tr>
              <tr><td><b>导出</b></td><td>按多种格式导出账号（含 CLI 请求头模板，可直接用于第三方工具）。</td></tr>
            </tbody>
          </table>
        </section>

        <!-- 代理池 -->
        <section id="proxy" class="sec">
          <span class="kicker">NETWORK</span>
          <h2><span class="h-ico" v-html="secIcon('proxy')"></span>代理池</h2>
          <p class="lead">注册与打码的网络出口统一管理。</p>
          <ul class="lst">
            <li><b>导入格式</b>：<code>scheme://user:pass@host:port</code>，每行一条；支持 socks5 / http / https 代理。</li>
            <li><b>批量操作</b>：全选 / 反选 / 批量删除 / 一键测试连通（并发测速，显示延迟）。</li>
            <li><b>健康管理</b>：失败代理自动冷却（<code>proxy_cooldown_minutes</code>），恢复后自动回池；可查看每条的失败次数与最后使用时间。</li>
            <li><b>轮换策略</b>：随机 / 顺序两种取用模式（<code>proxy_pick_mode</code>）。</li>
            <li><b>IP 隔离</b>：同一代理同时注册数上限（<code>max_per_ip</code>）防止同 IP 密集注册被风控。</li>
          </ul>
        </section>

        <!-- WARP -->
        <section id="warp" class="sec">
          <span class="kicker">NETWORK</span>
          <h2><span class="h-ico" v-html="secIcon('warp')"></span>WARP 代理</h2>
          <p class="lead">将 Cloudflare WARP 转为可用代理，并支持自动旋转。</p>
          <ul class="lst">
            <li><b>注册 / 登录 WARP</b>：获取 WARP 会话（wg 密钥），生成本地代理端点。</li>
            <li><b>旋转模式</b>：<code>none</code>（固定出口）或按间隔自动重连换 IP（<code>warp_rotate_every</code>）。</li>
            <li><b>合并入池</b>：一键把 WARP 代理并入代理池参与注册轮换。</li>
          </ul>
        </section>

        <!-- EDU -->
        <section id="edu" class="sec">
          <span class="kicker">MAIL</span>
          <h2><span class="h-ico" v-html="secIcon('edu')"></span>EDU 邮箱</h2>
          <p class="lead">使用自有 Cloudflare 域名批量创建可收验证码的临时邮箱（EDU 域名池）。</p>
          <ul class="lst">
            <li><b>CF 账号接入</b>：粘贴 Cloudflare API Token（需 Zone / DNS 编辑权限），加载并探测账号下的域名。</li>
            <li><b>域名池管理</b>：多账号多域名，勾选参与注册的域名；切换账号后重新「加载并探测」。</li>
            <li><b>自动取码</b>：注册时自动在所选域名下创建 <code>邮箱@域名</code>，邮件到达后自动解析验证码填入注册流程。</li>
            <li><b>已开通入池</b>：已验证的邮箱可批量入池复用，减少重复创建。</li>
            <li><b>凭证安全</b>：CF 凭证仅存本机 Application Support，不落浏览器缓存。</li>
          </ul>
        </section>

        <!-- iCloud -->
        <section id="icloud" class="sec">
          <span class="kicker">MAIL</span>
          <h2><span class="h-ico" v-html="secIcon('icloud')"></span>iCloud 邮箱</h2>
          <p class="lead">两种来源：iCloud 取码平台（隐私邮箱 API）与 iCloud 登录 / IMAP 直连。</p>
          <ul class="lst">
            <li><b>取码平台接入</b>：填写平台地址 / API Key / 项目标识，保存后「测试连通」；注册页邮箱模式选「iCloud 取码平台」即自动取号 + 轮询验证码。</li>
            <li><b>iCloud 登录态</b>：Apple 账号登录（含 2FA），用于账号级邮箱操作。</li>
            <li><b>IMAP 登录</b>：保存 IMAP 凭证，注册验证码经 IMAP 收取。</li>
            <li><b>邮箱池同步</b>：把 iCloud 隐私邮箱批量同步进本地池，注册时按勾选账号取号。</li>
          </ul>
        </section>

        <!-- 插件中心 -->
        <section id="plugins" class="sec">
          <span class="kicker">ENGINES</span>
          <h2><span class="h-ico" v-html="secIcon('plugins')"></span>插件中心 / 打码</h2>
          <p class="lead">三款内置 Turnstile 打码引擎，本地与容器双模式，多架构二进制内置。</p>
          <table class="tbl">
            <thead><tr><th>引擎</th><th>实现</th><th>实测速度（真实挑战）</th><th>特点</th></tr></thead>
            <tbody>
              <tr><td><b>VeloraTurn</b></td><td>Go + 浏览器池</td><td>Windows 本机 <b>3.2s</b> 最快最稳</td><td>standby 预热池、无扩展加载，启动路径最轻</td></tr>
              <tr><td><b>Auralith</b></td><td>Go + go-rod</td><td>Linux/容器 <b>5.5s</b>；Windows 4.2s</td><td>x.ai 深度特调（patch / antiDebug），容器环境最优</td></tr>
              <tr><td><b>EzSolver</b></td><td>Python + nodriver</td><td>本机 9.4s（波动大）</td><td>兼容性最好，跨平台行为一致</td></tr>
            </tbody>
          </table>
          <h3>模式说明</h3>
          <ul class="lst">
            <li><b>本地模式</b>：Windows/macOS 直接调用本机 Chrome（自动探测路径，无需配置）；无需 Docker。</li>
            <li><b>容器模式</b>：Docker 部署一体容器（Xvfb + Chromium + 引擎），多引擎共享，Linux 生产环境首选。</li>
            <li><b>代理跟随</b>：三款引擎均支持把注册代理透传给 Chrome（本地 CONNECT 中继），打码 IP 与注册 IP 一致，避免风控误判。</li>
            <li><b>并发热调</b>：注册页并发变化时自动同步到引擎 worker 数（无需重启）。</li>
          </ul>
        </section>

        <!-- Docker -->
        <section id="docker" class="sec">
          <span class="kicker">INFRA</span>
          <h2><span class="h-ico" v-html="secIcon('docker')"></span>Docker 管理</h2>
          <p class="lead">容器化打码环境：一键安装、部署、升级。</p>
          <ul class="lst">
            <li><b>环境自检</b>：自动检测 Docker 守护进程、WSL2 / 虚拟机平台前置；缺失时给出精确修复指引（新电脑首次会提示重启系统一次）。</li>
            <li><b>安装 Docker</b>：一键 winget 静默安装 Docker Desktop（流式进度日志），装完自动启动并等待引擎就绪。</li>
            <li><b>一键部署打码容器</b>：创建一体容器（映射 8192/8193/8194），容器内自动安装 Chromium/Xvfb；首次约 3–8 分钟，镜像固化后秒级复用。</li>
            <li><b>引擎版本</b>：镜像版本号（v7 起）锁定引擎二进制，升级引擎后自动重建镜像。</li>
          </ul>
        </section>

        <!-- 设置 -->
        <section id="settings" class="sec">
          <span class="kicker">CONFIG</span>
          <h2><span class="h-ico" v-html="secIcon('settings')"></span>设置 / 打码通道</h2>
          <p class="lead">全局默认值、打码通道与账号入库开关。</p>
          <table class="tbl">
            <thead><tr><th>配置</th><th>明细</th></tr></thead>
            <tbody>
              <tr><td><b>打码通道 provider</b></td><td>默认打码引擎（ezsolver / veloraturn / auralith / 三方服务），注册页可临时覆盖。</td></tr>
              <tr><td><b>引擎地址 / 超时 / 重试</b></td><td>各引擎 URL、单次超时秒数与失败重试次数。</td></tr>
              <tr><td><b>自动入库 / 导出</b></td><td>注册成功自动写入账号库（auto_import）与自动导出（auto_export）开关。</td></tr>
              <tr><td><b>注册默认值</b></td><td>默认数量 / 并发 / 邮箱模式 / 系统类型 / 浏览器。</td></tr>
              <tr><td><b>主题</b></td><td>商务白 / 暗金。</td></tr>
            </tbody>
          </table>
        </section>

        <!-- FAQ -->
        <section id="faq" class="sec">
          <span class="kicker">FAQ</span>
          <h2><span class="h-ico" v-html="secIcon('faq')"></span>常见问题</h2>
          <div class="qa">
            <div class="q"><b>Q：打码一直失败怎么办？</b></div>
            <div class="a">先看引擎健康与日志（插件中心 / 引擎日志）：<br />① <code>no chrome found</code> → 未找到 Chrome，请安装 Chrome 或检查 CHROME_PATH；<br />② 挑战 iframe 不渲染（cfIframes=0）→ 引擎二进制过旧，升级到 v7；<br />③ 走代理失败 → 确认代理可用（代理池先测通）。</div>
          </div>
          <div class="qa">
            <div class="q"><b>Q：注册提示「创建邮箱失败」？</b></div>
            <div class="a">Mail.tm 免费服务对批量创建限流。换 EDU 域名池 / iCloud 邮箱，或降低并发、加大任务间隔。</div>
          </div>
          <div class="qa">
            <div class="q"><b>Q：测试对话被 426 拒绝？</b></div>
            <div class="a">Grok CLI 网关按 <code>x-grok-client-version</code> 判客户端新旧。升级 Grok-Nobody 到最新版（内置版本与官方锁步）。</div>
          </div>
          <div class="qa">
            <div class="q"><b>Q：Docker 检测不到？</b></div>
            <div class="a">确认「虚拟机平台」功能已启用且系统已重启（Hyper-V 加载）；在 Docker 管理页看自检结论，按提示操作后重试。</div>
          </div>
          <div class="qa">
            <div class="q"><b>Q：代理与打码的关系？</b></div>
            <div class="a">建议打码跟随注册代理（captcha_proxy_mode=registration）：挑战与注册同出口 IP，风控一致性最好。</div>
          </div>
          <div class="qa">
            <div class="q"><b>Q：数据存在哪里？</b></div>
            <div class="a">账号库、代理池、配置均保存在本机（不依赖网络服务），导出/备份请使用「导出」功能。</div>
          </div>
        </section>

        <div class="foot">
          Grok-Nobody · Grok 注册工作台 —— 如需支持，请在「设置」页查看版本信息后联系部署方。
        </div>
      </main>
    </div>
  </div>
</template>

<style scoped>
.guide { max-width: 1180px; margin: 0 auto; padding: 8px 4px 40px; }

/* Hero */
.hero { border-radius: 18px; padding: 34px 36px 30px; margin-bottom: 26px;
  background: linear-gradient(135deg, color-mix(in srgb, var(--accent) 14%, transparent), color-mix(in srgb, var(--accent) 3%, transparent) 55%), var(--card);
  border: 1px solid color-mix(in srgb, var(--accent) 28%, transparent); }
.hero-badge { display: inline-block; font-size: 11px; letter-spacing: .12em; font-weight: 700;
  color: var(--accent); border: 1px solid color-mix(in srgb, var(--accent) 40%, transparent);
  border-radius: 999px; padding: 3px 12px; margin-bottom: 14px; }
.hero h1 { margin: 0 0 8px; font-size: 26px; letter-spacing: .01em; }
.hero-sub { margin: 0 0 18px; color: var(--text-dim); line-height: 1.7; font-size: 13.5px; }
.hero-chips { display: flex; flex-wrap: wrap; gap: 8px; }
.chip { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; padding: 7px 14px;
  border-radius: 999px; border: 1px solid var(--border); background: var(--bg); color: var(--text);
  cursor: pointer; transition: all .15s; }
.chip:hover { border-color: var(--accent); color: var(--accent); transform: translateY(-1px); }

/* Layout */
.doc-layout { display: grid; grid-template-columns: 216px 1fr; gap: 26px; align-items: start; }
.toc { position: sticky; top: 14px; display: flex; flex-direction: column; gap: 2px;
  padding: 14px 12px; border: 1px solid var(--border); border-radius: 14px; background: var(--card); }
.toc-title { font-size: 11px; font-weight: 700; letter-spacing: .1em; color: var(--text-dim); padding: 0 8px 8px; }
.toc-item { display: flex; align-items: center; gap: 8px; font-size: 13px; padding: 7px 8px; border-radius: 8px;
  color: var(--text); text-decoration: none; transition: background .12s; }
.toc-item:hover { background: color-mix(in srgb, var(--accent) 10%, transparent); color: var(--accent); }
.toc-ico { font-size: 14px; width: 18px; text-align: center; display: inline-flex; align-items: center; justify-content: center; }
.gico { display: inline-block; flex-shrink: 0; }
.h-ico { display: inline-flex; align-items: center; margin-right: 6px; color: var(--accent); vertical-align: -2px; }
.toc-note { margin-top: 10px; padding: 8px 10px; font-size: 11.5px; line-height: 1.6; color: var(--text-dim);
  border-radius: 8px; background: color-mix(in srgb, var(--accent) 6%, transparent); }

/* Docs */
.docs { min-width: 0; }
.sec { padding: 22px 24px 24px; margin-bottom: 18px; border: 1px solid var(--border);
  border-radius: 16px; background: var(--card); scroll-margin-top: 14px; }
.sec h2 { margin: 2px 0 6px; font-size: 19px; }
.sec h3 { margin: 20px 0 8px; font-size: 14.5px; }
.lead { color: var(--text-dim); font-size: 13.5px; margin: 0 0 16px; line-height: 1.7; }

/* Steps */
.steps { display: flex; flex-direction: column; gap: 12px; margin-bottom: 16px; }
.step { display: flex; gap: 14px; padding: 14px 16px; border-radius: 12px;
  background: color-mix(in srgb, var(--accent) 5%, transparent); border: 1px solid color-mix(in srgb, var(--accent) 16%, transparent); }
.step-no { flex: none; width: 30px; height: 30px; border-radius: 50%; display: grid; place-items: center;
  font-weight: 800; color: #fff; background: var(--accent); font-size: 14px; }
.step p { margin: 4px 0 0; font-size: 12.5px; color: var(--text-dim); line-height: 1.65; }
.step code, .tbl code { background: color-mix(in srgb, var(--accent) 10%, transparent); padding: 1px 6px;
  border-radius: 6px; font-size: 12px; }

/* Table */
.tbl { width: 100%; border-collapse: collapse; font-size: 12.8px; margin: 10px 0 6px; }
.tbl th { text-align: left; font-size: 11.5px; letter-spacing: .05em; color: var(--text-dim);
  padding: 8px 10px; border-bottom: 1px solid var(--border); }
.tbl td { padding: 9px 10px; border-bottom: 1px solid color-mix(in srgb, var(--border) 60%, transparent); line-height: 1.65; vertical-align: top; }
.tbl tr:last-child td { border-bottom: none; }
.tbl b { color: var(--text); }

/* Lists & callouts */
.lst { margin: 8px 0 4px; padding-left: 20px; }
.lst li { font-size: 13px; line-height: 1.75; margin-bottom: 6px; }
.lst b { color: var(--text); }
.lst code { background: color-mix(in srgb, var(--accent) 10%, transparent); padding: 1px 6px; border-radius: 6px; font-size: 12px; }
.callout { margin-top: 14px; padding: 12px 14px; border-radius: 10px; font-size: 12.8px; line-height: 1.7;
  border: 1px solid color-mix(in srgb, #e6a23c 40%, transparent);
  background: color-mix(in srgb, #e6a23c 8%, transparent); }

/* FAQ */
.qa { margin-bottom: 12px; padding: 12px 14px; border-radius: 10px; background: var(--bg);
  border: 1px solid var(--border); }
.qa .q { font-size: 13.5px; margin-bottom: 6px; }
.qa .a { font-size: 12.8px; color: var(--text-dim); line-height: 1.7; }
.qa code { background: color-mix(in srgb, var(--accent) 10%, transparent); padding: 1px 6px; border-radius: 6px; font-size: 12px; }

.foot { text-align: center; color: var(--text-dim); font-size: 12px; padding: 18px 0 4px; }

@media (max-width: 900px) {
  .doc-layout { grid-template-columns: 1fr; }
  .toc { position: static; flex-direction: row; flex-wrap: wrap; }
}
</style>
