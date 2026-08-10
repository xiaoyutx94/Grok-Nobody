package plugins

// Docker 模式下的浏览器环境。
//
// 三个免费打码器都必须用 headful Chrome：实测 --headless=new 的 UA 里带
// HeadlessChrome、screen 报 800x600，Cloudflare 明确把 headless 列为拦截目标；
// 而在 macOS 上把窗口隐藏（Cmd+H）会让 visibilityState 变 hidden、
// requestAnimationFrame 完全停摆、定时器掉到 2~4%，打码根本跑不完。
//
// 所以「浏览器不出现在桌面上」的正解是 Docker：容器里跑 Xvfb 虚拟屏，
// 浏览器仍是 headful（Turnstile 看到的一切正常），但整块屏幕是虚拟的，
// 宿主桌面上什么都不会弹。这也正是这些打码器原本的部署形态
// （EzSolver 的描述就写着 nodriver + Xvfb）。
//
// 原实现的 Docker 模式用 python:3.11-slim / debian:bookworm-slim 直接起服务，
// 镜像里既没有 Chromium 也没有 Xvfb，容器能起但一次也解不出来。

const (
	// dockerChromePath 容器内 Chrome 可执行文件位置。
	// 优先 google-chrome-stable（amd64 生产打码机实测 solve p50≈5.5s，
	// 远快于 debian chromium 的 18s+），下载失败时 bootstrap 会兜底装
	// debian chromium 并做符号链接，保证此路径恒可用。
	dockerChromePath = "/usr/bin/google-chrome-stable"

	// dockerBootstrapVersion 是容器 bootstrap 脚本的版本标签（写入容器 label）。
	// 部署时只有同版本容器才复用（旧版本容器可能缺依赖/参数，直接重建）。
	// v5：bootstrap 改为安装 google-chrome-stable（按容器架构选 amd64/arm64 deb），
	//     三个引擎对齐 amd64 生产方案（Turnstile 补丁目录 + context 池 +
	//     soft-timeout/launch-concurrency 等环境变量）。
	// v6：auralith 关闭 leanMem（AURALITH_LEAN_MEM=0）——leanMem 会给 Chrome
	//     加 js-flags --max-old-space-size=256，Turnstile 大 WASM 挑战在
	//     256MB 堆下频繁 GC，solve 从 7.8s 拖到 10.7s（实测）。v5 容器需重建。
	// v7: 引擎二进制全面重编（auralith 600010 修复 + veloraturn/auralith
	// detectMemMB Windows 分支 + 最新源码），旧镜像（v6）里固化的是
	// 7月24/25 旧二进制，必须 bump 版本才能触发重建。
	dockerBootstrapVersion = "v7"

	// dockerBrowserBootstrap 首次启动时装 Chrome + Xvfb。
	// 已装则跳过（容器重启复用同一层，apt 只跑一次）。
	// DEBIAN_FRONTEND 避免 tzdata 之类交互卡住。
	// chrome-stable 下载失败时回落 debian chromium 并软链到标准路径。
	dockerBrowserBootstrap = "set -e; " +
		"if [ ! -x " + dockerChromePath + " ]; then " +
		"export DEBIAN_FRONTEND=noninteractive; " +
		"apt-get update -qq; " +
		"apt-get install -y -qq --no-install-recommends " +
		"curl xvfb xauth ca-certificates fonts-liberation libnss3 libatk1.0-0 " +
		"libatk-bridge2.0-0 libcups2 libdrm2 libxkbcommon0 libxcomposite1 " +
		"libxdamage1 libxfixes3 libxrandr2 libgbm1 libasound2 procps " +
		"libu2f-udev libxss1 libxtst6; " +
		"ARCH=$(uname -m); " +
		"if [ \"$ARCH\" = \"x86_64\" ]; then CHROME_DEB_URL=https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb; " +
		"else CHROME_DEB_URL=https://dl.google.com/linux/direct/google-chrome-stable_current_arm64.deb; fi; " +
		"curl -fsSL -o /tmp/chrome.deb \"$CHROME_DEB_URL\" || true; " +
		"if [ -s /tmp/chrome.deb ]; then dpkg -i /tmp/chrome.deb 2>/dev/null || apt-get -f install -y -qq; rm -f /tmp/chrome.deb; fi; " +
		"if [ ! -x " + dockerChromePath + " ]; then " +
		"apt-get install -y -qq --no-install-recommends chromium || true; " +
		"if [ -x /usr/bin/chromium ]; then ln -sf /usr/bin/chromium " + dockerChromePath + "; fi; " +
		"fi; " +
		"fi; "

	// dockerXvfbRun 在虚拟屏里跑服务。1280x800x24 与真实桌面同量级，
	// 屏幕尺寸异常本身就是打码平台的特征之一（headless 的 800x600 就是反例）。
	dockerXvfbRun = "xvfb-run -a --server-args='-screen 0 1280x800x24' "
)
