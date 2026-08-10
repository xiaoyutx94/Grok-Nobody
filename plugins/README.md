# plugins/

Plugin center directory. It hosts the three captcha engines and a helper panel:

| Plugin | Purpose | Engine |
|--------|---------|--------|
| `auralith/` | Auralith captcha engine | Go daemon `auralithd` (local / docker) |
| `veloraturn/` | VeloraTurn captcha engine | Go daemon `veloraturn` (local / docker) |
| `ezsolver/` | EzSolver captcha engine | Python `service.py` / `solver.py` (local / docker) |
| `icloud-panel/` | iCloud mailbox helper panel | platform binaries |

## Why is this directory empty?

The three **captcha engine implementations are proprietary and are not shipped with this open-source repository**; `icloud-panel` is a compiled artifact and is also not included. This directory only carries this note.

- Engines talk to the backend over local ports: EzSolver `:8192`, VeloraTurn `:8193`, Auralith `:8194`
- Backend integration code lives in `backend/internal/pkg/captcha/` (HTTP clients) and `backend/internal/pkg/grokregister/`
- Bring your own engines (or alternative implementations) by exposing the same ports
