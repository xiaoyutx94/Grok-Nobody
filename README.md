# Grok-Nobody

A standalone desktop **Grok registration workbench**: batch account registration, EDU email provisioning, captcha plugin orchestration and iCloud email automation — all in a local desktop app with a web UI.

## Architecture

- **Frontend**: Vue 3 + TypeScript + Vite + Tailwind
- **Backend**: Go (embedded `grokregister` / `cfemail` packages + local JSON settings)
- **Desktop shell**: native HTTP API + web frontend; macOS is packaged as a signed `.app`
- **Plugin center**: EzSolver / VeloraTurn / Auralith engines, with **local** and **docker** install modes

## Layout

```
grok-nobody/
  assets/icon.svg
  backend/          # Go module
  frontend/         # Vue app
  plugins/          # engine plugins (see plugins/README.md)
  docs/             # architecture and release documentation
```

## Development

> Build/package scripts are distributed privately and are not part of this repository; equivalent commands are listed below.

Frontend dev server:

```bash
cd frontend && pnpm install && pnpm dev
# UI   http://127.0.0.1:5179
```

Backend API + UI (served at http://127.0.0.1:17890):

```bash
cd frontend && pnpm install && pnpm build
mkdir -p ../backend/internal/web/dist
rsync -a --delete dist/ ../backend/internal/web/dist/
cd ../backend && go run ./cmd/umbraforge -root ..
```

---

## Usage Guide

### Quick start — your first Grok account in ~3 minutes

1. **Prepare a captcha engine** — open the **Plugins center** and install *EzSolver / VeloraTurn / Auralith* in **local** mode (Windows automatically uses the local Chrome; no Docker required). The engine is ready when its health shows **healthy**.
2. **Import proxies** — on the **Proxy pool** page, paste proxies in `scheme://user:pass@host:port` format (socks5/http supported), then click **batch import**. Registration picks proxies randomly by default; failed proxies are cooled down automatically.
3. **Start registration** — on the **Grok Register** page enter a count (e.g. 50), choose an email mode (*Mail.tm* or *EDU domain pool*) and a captcha channel (*VeloraTurn* is fastest on Windows), then click **Start**. The task runs with auto concurrency, auto pause and auto import.

> The register page can switch between **Business White / Dark Gold** themes; your preference is remembered.

### Home / monitoring

| Card / area | What it shows |
|---|---|
| **Docker status** | Docker daemon / virtualization prerequisites (WSL2, Hyper-V), CPU cores, memory and current captcha slots; precise repair hints when prerequisites are missing (e.g. "system restart required"). |
| **Captcha engine health** | Local/container run state and health checks for the three engines (EzSolver / VeloraTurn / Auralith). |
| **Registration task status** | Progress, success rate, imported count and elapsed time of the latest task. |
| **Proxy pool overview** | Available proxy count and format statistics. |
| **Quick entries** | One-click jumps: start registration, import proxies, install captcha, deploy Docker container. |

### Grok registration

All parameters can be filled on the page or reused as batch task configuration.

| Parameter | Description | Suggested |
|---|---|---|
| `count` | Total accounts for this task. | 50–200 |
| `concurrency` | Simultaneous registration threads. Too high amplifies email/proxy rate limiting; too low starves throughput. | 3–8 |
| `task/step interval` | Delay (seconds) between tasks and between registration steps, to lower risk-control pacing. | 0–3 |
| `email mode` | **Mail.tm** disposable inbox (no config) · **EDU domain pool** (your own CF domains, multi-select) · **iCloud code platform** (auto number allocation + code polling) · **Outlook import** (paste your own credentials in batch). | per resource |
| `captcha channel` | **VeloraTurn** (fastest on Windows, ~3.2s) · **Auralith** (fastest on Linux/container, ~5.5s) · **EzSolver** (Python, best compatibility) · third-party (YesCaptcha / NextCaptcha). | Windows→vt, Linux→au |
| `system type` | Emulated registration device OS (macOS / Windows), affects browser UA fingerprint. | macos |
| `browser` | chrome / chromium. | chrome |
| `skip email verification` | Skips waiting for the verification code (debugging only; account will be unusable). | off |
| `proxy source` | Random from pool or a single specified proxy; captcha can **follow the registration proxy** or connect directly (following is recommended so IPs match). | pool + follow |

**Automation & fault tolerance**

- **Auto pause** — pauses 5 minutes after consecutive failures exceed the threshold (default 10), avoiding idle spinning when proxies/mailboxes are all down.
- **Proxy cooldown** — a proxy failing 3 times in a row is cooled down for 10 minutes; success lifts it automatically.
- **Auto import** (`auto_import` in Settings) — successfully registered accounts are written into the account store (email + password + SSO credentials) automatically.
- **Auto export** (`auto_export`) — exports results to a file in the configured format on success.
- **Live logs** — the task log scrolls in real time; every failure reason is inspectable (mailbox creation failed / captcha timeout / page change, etc.).

> **Success-rate tuning**: if "mailbox creation failed" dominates, the disposable-mail service is rate limited — switch to an EDU domain pool or lower concurrency; if "captcha failed" dominates, first check engine health and proxy connectivity.

### Account management

- **Account list** — email, status, latest test result and bound proxy in one screen; search / filter / pagination.
- **One-click copy** — email and password copy with a single click.
- **Test chat** — real x.ai conversation using the account's OAuth credentials (official Grok CLI fingerprint: UA `grok-shell/…` + `x-xai-token-auth` + `x-grok-client-version`), with streaming replies, to verify account usability.
- **Verify credentials** — re-validate SSO/OAuth credential validity (expired ones are flagged automatically).
- **Re-fetch credentials** — re-login to x.ai when the session is invalid to obtain fresh credentials.
- **Update / delete** — edit email/password/notes; delete accounts (batch supported).
- **Export** — multiple export formats (including a CLI request-header template usable by third-party tools).

### Proxy pool

- **Import format** — `scheme://user:pass@host:port`, one per line; socks5 / http / https supported.
- **Batch operations** — select all / invert / batch delete / one-click connectivity test (concurrent speed test with latency shown).
- **Health management** — failed proxies cool down automatically (`proxy_cooldown_minutes`) and return to the pool when recovered; per-proxy failure count and last-use time are visible.
- **Rotation** — random or sequential pickup (`proxy_pick_mode`).
- **IP isolation** — per-proxy concurrent registration cap (`max_per_ip`) prevents dense same-IP registration being flagged.

### WARP proxy

- **Register / login WARP** — obtain a WARP session (wg key) and generate a local proxy endpoint.
- **Rotation** — `none` (fixed egress) or automatic reconnect/rotate on an interval (`warp_rotate_every`).
- **Merge into pool** — add WARP proxies to the proxy pool for registration rotation with one click.

### EDU mailbox

- **CF account connect** — paste a Cloudflare API Token (Zone / DNS edit permission required), load and probe domains under the account.
- **Domain pool** — multiple accounts/domains, tick the domains to use for registration; re-load/probe after switching accounts.
- **Auto code fetching** — during registration the app creates `email@domain` under the selected domains, parses the incoming verification mail and fills the code into the registration flow.
- **Verified pool** — verified addresses can be batch-imported and reused, reducing duplicate creation.
- **Credential safety** — CF credentials are stored only in local Application Support, never in browser caches.

### iCloud mailbox

- **Code platform** — fill in platform URL / API Key / project id, then "test connectivity"; selecting the "iCloud code platform" email mode on the register page auto-allocates numbers and polls codes.
- **iCloud login state** — Apple account login (2FA supported) for account-level mailbox operations.
- **IMAP login** — store IMAP credentials; verification codes are received over IMAP.
- **Mailbox pool sync** — batch-sync iCloud privacy mailboxes into the local pool for allocation per selected account.

### Plugin center / captcha

| Engine | Implementation | Measured speed (real challenges) | Notes |
|---|---|---|---|
| **VeloraTurn** | Go + browser pool | Windows local **3.2s** — fastest & most stable | standby warm pool, no extension loading, lightest startup |
| **Auralith** | Go + go-rod | Linux/container **5.5s**; Windows 4.2s | deeply tuned for x.ai (patch / antiDebug), best in containers |
| **EzSolver** | Python + nodriver | local 9.4s (more variance) | best compatibility, consistent cross-platform behavior |

- **Local mode** — calls the local Chrome directly on Windows/macOS (auto-detected path, no configuration); no Docker needed.
- **Container mode** — a Docker all-in-one container (Xvfb + Chromium + engine), shared by all engines; the choice for Linux production.
- **Proxy follow** — all three engines can tunnel the registration proxy to Chrome (local CONNECT relay) so captcha IP matches registration IP and risk control sees consistency.
- **Concurrency hot-tuning** — engine worker counts sync automatically when registration concurrency changes (no restart).

### Docker management

- **Environment self-check** — detects the Docker daemon and WSL2 / virtual-machine platform prerequisites, with precise repair guidance (first-time machines get a one-time reboot hint).
- **Install Docker** — one-click silent winget install of Docker Desktop (streaming progress logs), auto-start and engine-ready waiting.
- **One-click captcha container deploy** — creates the all-in-one container (ports 8192/8193/8194) with Chromium/Xvfb installed inside; ~3–8 minutes the first time, near-instant reuse once the image is cached.
- **Engine version pinning** — image versions (v7+) pin the engine binaries; upgrading engines rebuilds the image automatically.

### Settings / captcha channel

- **Captcha provider** — default engine (ezsolver / veloraturn / auralith / third-party); overridable on the register page.
- **Engine URL / timeout / retry** — per-engine URL, per-attempt timeout and failure retry count.
- **Auto import / export** — toggles for `auto_import` (accounts) and `auto_export` (files).
- **Registration defaults** — default count / concurrency / email mode / system type / browser.
- **Theme** — Business White / Dark Gold.

### FAQ

**Q: Captcha keeps failing?**
Check engine health and logs (Plugin center → engine logs):
1. `no chrome found` → Chrome missing; install Chrome or check `CHROME_PATH`;
2. challenge iframe not rendering (`cfIframes=0`) → engine binary too old, upgrade to v7;
3. proxy failures → confirm the proxy works (test it in the proxy pool first).

**Q: "Mailbox creation failed" during registration?**
Mail.tm's free service rate-limits batch creation. Switch to an EDU domain pool / iCloud mailbox, or lower concurrency and increase task intervals.

**Q: Test chat rejected with 426?**
The Grok CLI gateway judges client freshness via `x-grok-client-version`. Upgrade Grok-Nobody to the latest version (the bundled version tracks the official release).

**Q: Docker not detected?**
Confirm the "Virtual Machine Platform" feature is enabled and the system has been restarted (Hyper-V loaded); check the self-check conclusion on the Docker page and retry.

**Q: How do proxy and captcha relate?**
Follow the registration proxy for captcha (`captcha_proxy_mode=registration`): the challenge and the registration share the same egress IP, which is best for risk-control consistency.

**Q: Where is my data stored?**
The account store, proxy pool and settings all live on the local machine (no network service dependency); use the export features for backups.

---

## Data directories

- macOS: `~/Library/Application Support/UmbraForge` *(legacy internal directory name, kept for data compatibility)*
- Windows: `%APPDATA%/UmbraForge`
- Linux: `~/.config/umbraforge`

## Release model

The protected Windows release pipeline (obfuscated executable + encrypted `plugins.ufp` bundle) is described in [docs/WINDOWS_PROTECTION.md](docs/WINDOWS_PROTECTION.md). Release binaries require a trusted Authenticode certificate from the publisher; this repository does not forge production certificates.

## Lineage

The registration and EDU email engines are derived from the Sub2API project as an independent Go module.
