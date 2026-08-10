# plugins/

本目录是插件中心目录,包含三种打码引擎与辅助面板:

| 插件 | 用途 | 引擎形态 |
|------|------|----------|
| `auralith/` | Auralith 打码引擎 | Go 守护进程 `auralithd`(local / docker) |
| `veloraturn/` | VeloraTurn 打码引擎 | Go 守护进程 `veloraturn`(local / docker) |
| `ezsolver/` | EzSolver 打码引擎 | Python `service.py` / `solver.py`(local / docker) |
| `icloud-panel/` | iCloud 邮箱辅助面板 | 平台二进制 |

## 为什么是空的?

三种**打码引擎的源码与二进制为专有实现,不随本开源仓库发布**;`icloud-panel` 为编译产物,同样不随仓库发布。仓库仅保留本说明文件。

- 引擎运行时通过本机端口与后端通信:`EzSolver :8192`、`VeloraTurn :8193`、`Auralith :8194`
- 后端调用代码见 `backend/internal/pkg/captcha/`(HTTP 客户端)与 `backend/internal/pkg/grokregister/`
- 自建引擎或使用替代实现时,按上述端口与后端对接即可

如需自行构建引擎,请参考引擎各自的独立工程(不在本仓库内)。
