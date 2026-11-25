# codex-tool

codex-tool 是一个面向 macOS 的独立 Codex 桌面配置助手。它可以配置自定义模型供应方、注册本地插件市场，也提供一个实验性简体中文启动器。请按使用的功能安装官方 ChatGPT 桌面应用或 Codex CLI。

## 平台支持

| 平台 | 首版状态 |
| --- | --- |
| macOS · Apple Silicon（arm64） | 源码构建和命令行测试通过。中文界面功能处于实验阶段，依赖桌面客户端版本。 |
| macOS · Intel、Windows、Linux | 暂不支持。 |

## 构建和使用

可以从 [GitHub Releases](https://github.com/Minshenyao/codex-tool/releases) 下载预览版的 macOS arm64 压缩包。也可以安装 Go 1.26，从源码构建。进入含 `go.mod` 的目录后运行以下命令。

```sh
go build -trimpath -ldflags='-s -w' -o codex-tool .
./codex-tool
```

直接运行会显示交互菜单。也可通过 `./codex-tool --help` 查看命令。

| 命令 | 用途 |
| --- | --- |
| `relay` | 配置兼容 OpenAI Responses API 的自定义供应方和固定模型列表。 |
| `plugins` | 从 OpenAI 的公开插件仓库创建并注册本地市场快照。 |
| `plugins-status` | 查询已注册的本地插件市场。 |
| `install` | 在 `~/Applications` 创建使用官方 ChatGPT 图标的 `ChatGPT-中文.app`。 |
| `verify` | 查看实验性启动器报告的内部状态。界面语言请以窗口里实际显示的文字为准。 |
| `status` | 查看官方客户端、本地配置文件和启动器的位置。 |

### 自定义供应方

`relay` 使用固定的五个模型：`deepseek-v4-flash`、`deepseek-v4-pro`、`gpt-6-luna`、`glm-5.3`、`glm-5.3-flash`。它不会向中转站查询模型，也不会把其他模型写入目录。供应方需要支持 Codex 使用的 Responses API，并提供这五个模型 ID。

如供应方需要 API 密钥，输入**环境变量名**，例如 `MY_RELAY_API_KEY`。在运行本工具及使用该供应方的 Codex 进程中设置该变量。自定义带密钥的网络地址请使用 HTTPS。

确认后，工具更新 `$CODEX_HOME/config.toml`（默认 `~/.codex/config.toml`）并为本次配置生成新的模型目录 JSON。若已有配置文件，工具会先保存一份备份。迁移旧版明文密钥时，备份依然包含原有密钥，请保管好备份。重新打开 Codex 后可使用新配置。

### 本地插件市场

需要 Git 和支持 `plugin marketplace` 的 Codex CLI。`plugins` 从 `https://github.com/openai/plugins.git` 拉取源码，只保留本地源插件。克隆沿用 Git 和系统环境中的代理配置。

每次运行都会在选定目录的 `codex-tool-plugin-snapshots/` 下创建新快照，并注册独立名称。默认存放目录为 `~/.local/share/codex-zh/plugins`。已有快照和注册可继续使用。若要清理旧市场，先通过 Codex CLI 注销，再处理对应快照目录。

### 实验性中文启动器

`install` 仅创建启动器，并从已通过签名校验的官方 ChatGPT 应用复制其图标。请先退出官方客户端，再在 Finder 中打开 `~/Applications/ChatGPT-中文.app`。启动器依赖官方客户端随附的 Node 以及应用内部的语言设置。运行时会启用仅限本机的调试会话，处理窗口时可能使其刷新。调试会话无法建立时，可以从 Finder 正常打开官方客户端。

可运行 `./codex-tool verify` 查看内部状态。日志位于 `~/Library/Logs/ChatGPT-中文启动器.log`。停用时退出客户端，直接打开官方应用即可。要移除启动器，将 `~/Applications/ChatGPT-中文.app` 移到废纸篓。

从早期版本升级时，旧的 `Codex 中文.app` 不会自动删除；确认不再使用后可手动移到废纸篓。
