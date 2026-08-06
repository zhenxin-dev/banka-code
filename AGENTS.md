# Banka Code 开发指南

## 项目概览

**Banka Code** 是一个使用 Go 构建的 Coding Agent。

Agent 接收用户指令后进入迭代循环：调用 LLM → 解析工具调用 → 执行工具 → 将结果送回 LLM → 重复，直到模型不再请求工具。支持流式输出、CLI 单次执行和全屏终端交互模式。

- **语言**：Go
- **构建**：`go build`
- **测试**：`go test ./...`
- **TUI**：Bubble Tea / Bubbles / Lip Gloss / Glamour
- **LLM 协议**：OpenAI Responses API / OpenAI Chat Completions API / Anthropic Messages API

## 元规则

- 使用中文回复
- 先判断用户意图，再决定是回答、排查还是实施
- 只做用户明确要求的事；不擅自扩展范围
- 先搜索再修改，先验证再结束

## 技术约束

- 优先使用 Go 标准库，不引入不必要依赖
- 公共包、公共类型和公共函数需要有文档注释
- 错误要带清晰上下文，不吞错误
- 文件路径必须经过工作区安全校验
- 删除或移动文件前先确认它属于当前任务范围

## 架构

### 核心流程

```
用户输入
  → cmd/banka/main.go
  → agent.Run()
    → llm.Client.Generate()
    → 解析 tool_calls
    → tools.Registry.Execute()
    → 将工具结果追加到消息列表
    → 重复直到无工具调用
```

### 模块职责

| 模块 | 职责 |
|------|------|
| `cmd/banka/` | CLI 入口，参数解析，运行单次模式或交互模式 |
| `internal/agent/` | Agent 主循环 |
| `internal/config/` | `.env` 和环境变量配置加载 |
| `internal/instructions/` | 全局和项目 `AGENTS.md` 分层加载 |
| `internal/llm/` | OpenAI Responses / Chat / Anthropic 客户端 |
| `internal/mcp/` | MCP 配置、stdio/HTTP 客户端和能力适配 |
| `internal/messages/` | 会话消息模型 |
| `internal/prompt/` | 默认系统提示词 |
| `internal/skills/` | `SKILL.md` 发现和按需资源加载 |
| `internal/tools/` | 工具系统和内置工具 |
| `internal/tui/` | 全屏终端交互模式、流式渲染和内置命令 |

## Provider 支持

| Provider | `BANKA_PROVIDER` 值 | 状态 |
|----------|---------------------|------|
| OpenAI Responses API | `openai` | 已支持 |
| OpenAI Chat 兼容 API | `openai-chat` | 已支持 |
| Anthropic Messages API | `anthropic` | 已支持 |

## 工具系统

| 工具名 | 功能 | 关键细节 |
|--------|------|----------|
| `Bash` | 执行终端命令 | 默认离线 bubblewrap；沙箱外执行需用户批准 |
| `Read` | 读取文件 | 限 1MB 文本文件，支持行偏移和上限 |
| `Write` | 写入文件 | 自动创建父目录 |
| `Edit` | 局部编辑文件 | 精确替换，目标文本必须唯一 |
| `Glob` | 按 pattern 查找文件 | 最多 100 条结果 |
| `Grep` | 按正则搜索文件内容 | 支持 `content` / `files_with_matches` 输出模式 |
| `ApplyPatch` | 应用统一 diff | 完整补丁先校验，拒绝越界和符号链接补丁 |
| `WebFetch` | 读取公共网页 | HTTP(S) 文本、SSRF 防护、用户审批 |
| `AskUser` | 向用户提问 | 暂停 Agent，收到回答后恢复工具循环 |
| `Skill` | 加载技能 | 完整读取 `SKILL.md` 后按需读取资源 |

MCP 还会按服务器动态注册 tools，并提供 resources、resource templates 和 prompts 访问工具。所有文件操作通过 `tools.ResolveSafePath()` 校验词法路径和符号链接真实路径，确保不越出 `workspaceRoot`。Bash 与 MCP 子进程默认不继承 `BANKA_*` 凭据。

## 开发流程

复杂任务按这个顺序推进：

1. 理解：读现有实现，找类似代码。
2. 规划：拆成清楚的小步骤。
3. 测试：能补测试就优先补测试。
4. 实现：用尽量少的代码解决问题。
5. 验证：运行相关测试、构建或检查。
6. 总结：说明改了什么、为什么、怎么验证。

## 代码风格

- 文件名使用小写蛇形或短横线风格，遵循 Go 社区常规
- 包名短小、全小写
- 变量和函数名优先清晰，不为缩短而牺牲可读性
- 公共 API 使用 Go doc 注释
- 测试文件命名为 `*_test.go`
- 测试数据自包含，避免依赖外部环境

## 可用命令

```bash
go run ./cmd/banka              # 启动交互模式
go run ./cmd/banka "提示词"       # 单次执行模式
go test ./...                   # 运行测试
make test
make build
make build-all
```

## Git 规范

- 不主动提交，除非用户明确要求
- 提交前查看 `git status`、`git diff` 和近期提交风格
- 提交信息遵循 Conventional Commits
- 提交信息使用中文
- 不使用 `--no-verify`
- 不使用 `git reset --hard`、`git checkout --` 等破坏性命令

## 完成定义

- 用户请求的范围已覆盖
- 修改与 Go 项目风格一致
- `go test ./...` 通过
- `go test -race ./...` 通过
- `go vet ./...` 通过
- `make build` 通过
- 已说明未处理风险或迁移差异
