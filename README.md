# 🌸 Banka Code

**Banka Code** — 基于 Go 的 Coding Agent，配备千恋万花风格全屏终端界面。

名字来源于 [千恋＊万花](https://www.yuzu-soft.com/products/senren/)（Senren\*Banka）——柚子社十周年纪念作。在这座名为「穗织」的温泉小镇里，万花等待你的指令，一刀斩断一切繁琐的编码工作。

> ⚠️ **项目处于早期开发阶段，请勿用于生产环境。** 安全机制尚未完善，Bash 工具虽有沙箱但仍可能存在绕过手段。

## 特性

- **Go** — 标准库优先、原生二进制、部署简单
- **Agent 循环** — 多轮对话、工具调用，自动迭代直到任务完成
- **流式 TUI** — Markdown 渲染、命令面板、状态动画、ESC 中断和多轮会话
- **双模式** — 全屏 TUI & CLI 单次执行模式
- **多 Provider** — 支持 OpenAI Responses、OpenAI Chat Completions 兼容接口和 Anthropic Messages API
- **完整工具面** — 文件读写、搜索、补丁、命令、网页读取、用户提问和按需技能
- **权限审批** — 默认离线工作区沙箱，联网、沙箱外命令和未信任 MCP 调用需用户批准
- **上下文管理** — 会话检查点、回滚与模型摘要压缩
- **指令与技能** — 分层加载 `AGENTS.md`，发现并按需读取 `SKILL.md`
- **MCP** — stdio / Streamable HTTP，支持 tools、resources、resource templates 和 prompts
- **跨平台构建** — 一键编译 Linux / macOS / Windows 原生二进制

## 快速开始

```bash
# 配置环境变量
cp .env.example .env
# 编辑 .env，填入 API 配置

# 启动交互模式
go run ./cmd/banka

# 单次执行模式
go run ./cmd/banka "看看当前项目结构"

# 构建当前平台二进制
make build
```

## CLI 参数

```
banka              启动交互模式
banka <提示词>      单次执行模式，输出结果后退出
banka -h, --help   显示帮助
banka -v, --version 显示版本号
banka --permission-mode <模式>  设置 default、full-access 或 yolo
```

### 交互模式内置命令

| 命令 | 说明 |
|------|------|
| `/help` | 查看内置命令帮助 |
| `/clear` | 清空当前会话内容 |
| `/undo` | 回滚上一轮会话上下文，不修改工作区文件 |
| `/compact` | 将较早会话压缩为摘要，保留最近两轮原文 |
| `/permissions` | 使用选择菜单切换当前会话权限模式 |
| `/status` | 查看当前会话状态 |
| `/exit` | 退出 Banka Code |
| `/quit` | 退出 Banka Code |

## 环境变量

Go 版会读取工作区 `.env`，也支持直接使用进程环境变量。

```bash
BANKA_PROVIDER=openai        # openai | openai-chat | anthropic
BANKA_API_KEY=your-api-key   # 必填
BANKA_BASE_URL=https://...   # API 端点（可选，留空使用 Provider 默认值）
BANKA_MODEL=your-model-id    # 模型名称（必填）
BANKA_PERMISSION_MODE=default # default | full-access | yolo
```

### OpenAI Responses（默认）

`openai` provider 走 OpenAI Responses API：

```bash
# OpenAI
BANKA_PROVIDER=openai
BANKA_API_KEY=sk-...
BANKA_BASE_URL=https://api.openai.com/v1
BANKA_MODEL=gpt-4.1
```

### OpenAI Chat 兼容

第三方兼容服务使用 `openai-chat`：

```bash
# DeepSeek
BANKA_PROVIDER=openai-chat
BANKA_API_KEY=sk-...
BANKA_BASE_URL=https://api.deepseek.com
BANKA_MODEL=deepseek-v4-flash

# Ollama（本地）
BANKA_PROVIDER=openai-chat
BANKA_API_KEY=ollama
BANKA_BASE_URL=http://127.0.0.1:11434/v1
BANKA_MODEL=qwen3:8b

# GLM（智谱）、Kimi、MiniMax、Qwen（通义）、Xiaomi（MiMo）等
# 修改 BANKA_BASE_URL 和 BANKA_API_KEY 即可
```

### Anthropic

```bash
BANKA_PROVIDER=anthropic
BANKA_API_KEY=sk-ant-...
BANKA_BASE_URL=https://api.anthropic.com/v1
BANKA_MODEL=claude-sonnet-4-20250514
```

## 工具系统

| 工具 | 功能 | 说明 |
|------|------|------|
| **Bash** | 执行终端命令 | 30s 默认超时，可配置到 10 分钟；默认离线沙箱 |
| **Read** | 读取文件 | 限 1MB 文本文件，支持 `offset` / `limit` |
| **Write** | 写入文件 | 自动创建父目录 |
| **Edit** | 局部编辑文件 | 精确替换，要求目标文本唯一 |
| **Glob** | 按 pattern 查找文件 | 最多 100 条结果 |
| **Grep** | 按正则搜索文件内容 | 支持 `content` / `files_with_matches` 输出模式 |
| **ApplyPatch** | 应用统一 diff | 完整补丁先检查后应用，拒绝越界路径和符号链接 |
| **WebFetch** | 读取公共网页 | 仅 HTTP(S) 文本，阻止本地/私网地址并请求用户批准 |
| **AskUser** | 向用户提问 | Agent 暂停，TUI/CLI 回答后继续原工具循环 |
| **Skill** | 加载技能 | 先完整读取 `SKILL.md`，再按需读取技能资源 |

默认模式下，所有文件操作通过 `ResolveSafePath()` 校验词法路径和符号链接真实路径。Bash 会拒绝越界路径、提权命令、危险环境变量操作和越界重定向；检测到 bubblewrap 时隔离文件系统和网络。需要网络或工作区外访问时，模型必须设置 `sandbox_permissions=require_escalated` 并给出理由。

审批使用方向键选择菜单，支持“允许一次”“始终允许此类操作（本次会话）”和“拒绝”。“始终允许”只保存在当前进程内，不会写入配置文件。

| 权限模式 | 行为 |
|----------|------|
| `default` | 默认沙箱；工作区外、网络和未信任 MCP 操作按需审批 |
| `full-access` | 关闭文件、Bash 和网络沙箱；未信任 MCP 仍需审批 |
| `yolo` | 完全访问，并自动批准包括未信任 MCP 在内的所有权限请求 |

Bash 与 MCP 子进程不会继承 `BANKA_*` 模型配置和 API Key。MCP 配置若显式传入同名环境变量则视为用户授权。

## AGENTS.md 与 Skills

启动时按以下优先级加载指令，越靠后的文件优先级越高：

1. `~/.agents/AGENTS.md`
2. Git 项目根到当前工作目录逐层的 `AGENTS.md`
3. 同目录存在 `AGENTS.override.md` 时，用它替代该目录的 `AGENTS.md`

技能从以下目录递归发现，项目技能覆盖同名全局技能：

```text
~/.codex/skills/**/SKILL.md
~/.agents/skills/**/SKILL.md
<project>/.codex/skills/**/SKILL.md
<project>/.agents/skills/**/SKILL.md
```

系统提示只包含技能名称和描述。模型命中技能后调用 `Skill` 工具完整读取正文，技能引用的其他文件也通过该工具按需读取。

## MCP

Banka 合并读取 `~/.banka/mcp.json`、项目 `.mcp.json` 和项目 `.banka/mcp.json`，后者覆盖前者。兼容标准 `mcpServers` 字段和 Banka 的 `servers` 字段。

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]
    },
    "remote": {
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${MCP_TOKEN}"
      }
    }
  }
}
```

默认情况下，每次 MCP 工具、资源或 prompt 访问都需要批准。只有你明确控制并信任服务器时，才可在对应配置加入 `"trusted": true`。MCP 工具注册为 `mcp__<server>__<tool>`，并向服务器公布当前项目根目录。

## 项目结构

```
banka-code/
├── cmd/banka/                # Go CLI 入口
├── internal/
│   ├── agent/                # Agent 主循环
│   ├── config/               # 运行时配置
│   ├── instructions/         # AGENTS.md 分层加载
│   ├── llm/                  # OpenAI Responses / Chat / Anthropic 客户端
│   ├── mcp/                  # MCP 配置、客户端和能力适配
│   ├── messages/             # 会话消息模型
│   ├── prompt/               # 系统提示词
│   ├── skills/               # SKILL.md 发现与按需加载
│   ├── tools/                # 工具系统
│   └── tui/                  # 全屏终端交互模式
├── go.mod
├── go.sum
├── Makefile
├── .env.example
├── AGENTS.md
└── README.md
```

## 可用命令

```bash
go run ./cmd/banka              # 启动交互模式
go run ./cmd/banka "提示词"       # 单次执行模式
go test ./...                   # 运行测试
make build                      # 构建当前平台
make build-all                  # 构建 Linux/macOS/Windows amd64/arm64
```

## 技术栈

| 层 | 技术 |
|----|------|
| 语言 | Go |
| TUI | Bubble Tea / Bubbles / Lip Gloss / Glamour |
| LLM 协议 | OpenAI Responses / OpenAI Chat Completions / Anthropic Messages |
| 构建 | `go build` |

## 许可

[GPLv3](LICENSE)
