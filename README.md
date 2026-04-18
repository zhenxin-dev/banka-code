# 🌸 Banka Code

**Banka Code** — 基于 TypeScript + [Bun](https://bun.sh) 的 Coding Agent，配备千恋万花风格 TUI 终端界面。

名字来源于 [千恋＊万花](https://www.yuzu-soft.com/products/senren/)（Senren\*Banka）——柚子社十周年纪念作。在这座名为「穗织」的温泉小镇里，万花等待你的指令，一刀斩断一切繁琐的编码工作。

## 特性

- **TypeScript + Bun** — strict mode、零 `any`、极致性能
- **Agent 循环** — 流式输出、多轮对话、工具调用，自动迭代直到任务完成
- **千恋万花 TUI** — 樱粉暖橘配色、Ciallo 流光 Logo、灯笼走马灯动画
- **双模式** — TUI 交互模式 & CLI 单次执行模式
- **多 Provider** — OpenAI 兼容 / Anthropic 兼容 / Ollama
- **6 个工具** — Bash、Read、Write、Edit、Glob、Grep
- **跨平台构建** — 一键编译 Linux / macOS / Windows 原生二进制

## 快速开始

```bash
# 安装依赖
bun install

# 配置环境变量
cp .env.example .env
# 编辑 .env，填入 API 配置

# 启动 TUI
bun run start

# 单次执行模式
bun run src/index.ts "看看当前项目结构"
```

## CLI 参数

```
banka              启动 TUI 交互模式
banka <提示词>      单次执行模式，输出结果后退出
banka -h, --help   显示帮助
banka -v, --version 显示版本号
```

### TUI 内置命令

| 命令 | 说明 |
|------|------|
| `/exit` | 退出 TUI |
| `/quit` | 退出 TUI |

## 环境变量

使用 Bun 原生 `.env`，无需额外安装 `dotenv`。

```bash
BANKA_PROVIDER=openai        # openai | anthropic | ollama
BANKA_API_KEY=your-api-key   # ollama 可省略
BANKA_BASE_URL=https://...   # API 端点
BANKA_MODEL=your-model-id    # 模型名称（必填）
```

### OpenAI 兼容

```bash
BANKA_PROVIDER=openai
BANKA_API_KEY=your-api-key
BANKA_BASE_URL=https://api.example.com/v1
BANKA_MODEL=your-model-id
```

向 `${BANKA_BASE_URL}/chat/completions` 发起请求，兼容 OpenAI Chat Completions API。

### Anthropic 兼容

```bash
BANKA_PROVIDER=anthropic
BANKA_API_KEY=your-api-key
BANKA_BASE_URL=https://api.example.com/anthropic/v1
BANKA_MODEL=your-model-id
```

向 `${BANKA_BASE_URL}/messages` 发起请求，兼容 Anthropic Messages API（`anthropic-version: 2023-06-01`）。

### Ollama（本地）

```bash
BANKA_PROVIDER=ollama
BANKA_BASE_URL=127.0.0.1:11434
BANKA_MODEL=qwen3:8b
```

`BANKA_API_KEY` 可省略。自动补全为 `http://127.0.0.1:11434/v1/chat/completions`。

## 工具系统

| 工具 | 功能 | 说明 |
|------|------|------|
| **Bash** | 执行终端命令 | 30s 超时，输出截断 12KB |
| **Read** | 读取文件 | 限 1MB 以内文本文件 |
| **Write** | 写入文件 | 自动创建父目录 |
| **Edit** | 局部编辑文件 | 精确替换，要求目标文本唯一 |
| **Glob** | 按 pattern 查找文件 | 最多 100 条结果 |
| **Grep** | 按正则搜索文件内容 | 支持 `content` / `files_with_matches` 输出模式 |

所有文件操作均限制在工作区内，防止路径越界。工具调用在 TUI 中显示人类可读名 + 参数摘要，如 `Bash · ls -la`、`Read · src/tui/app.tsx`。

## 项目结构

```
banka-code/
├── src/
│   ├── agent/                # Agent 主循环
│   │   └── run-agent-loop.ts
│   ├── errors/               # 自定义错误层级
│   │   └── banka-error.ts
│   ├── messages/             # 会话消息模型
│   │   └── message.ts
│   ├── models/               # 模型客户端
│   │   ├── model-client.ts       # 统一接口
│   │   ├── create-model-client.ts # 工厂
│   │   ├── openai-model-client.ts
│   │   └── anthropic-model-client.ts
│   ├── prompt/               # 系统提示词
│   │   └── system-prompt.ts
│   ├── runtime/              # 运行时配置
│   │   └── runtime-config.ts
│   ├── shared/               # 共享工具函数
│   │   └── is-record.ts
│   ├── tools/                # 工具系统
│   │   ├── tool.ts               # 核心类型
│   │   ├── tool-registry.ts      # 注册表
│   │   ├── execute-tool-call.ts   # 调用执行器
│   │   ├── create-tools.ts       # 工具集工厂
│   │   ├── bash-tool.ts
│   │   ├── file-tools.ts         # Read / Write / Edit
│   │   ├── glob-tool.ts
│   │   ├── grep-tool.ts
│   │   └── safe-path.ts          # 路径安全校验
│   ├── tui/                  # TUI 界面
│   │   ├── run-tui.tsx           # 渲染入口
│   │   ├── app.tsx               # 主界面布局
│   │   ├── logo.tsx              # Ciallo 流光 Logo
│   │   ├── spinner.tsx           # Braille 动画
│   │   ├── theme.ts              # 千恋万花主题色
│   │   ├── output.ts             # 终端输出格式化
│   │   ├── message-format.ts     # 消息格式化
│   │   └── hitokoto.ts           # 一言 API
│   └── index.ts              # CLI 入口
├── scripts/
│   └── build.ts              # 跨平台二进制构建
├── package.json
├── tsconfig.json
├── bunfig.toml
├── .env.example
├── AGENTS.md
└── README.md
```

## 可用脚本

```bash
bun run start      # 启动 TUI
bun run check      # TypeScript 类型检查
bun run test       # 运行测试
bun run build      # 构建当前平台二进制
bun run build:all  # 构建全平台二进制
```

## 技术栈

| 层 | 技术 |
|----|------|
| 运行时 | [Bun](https://bun.sh) |
| 语言 | TypeScript（strict + `noUncheckedIndexedAccess` + `exactOptionalPropertyTypes`） |
| TUI 框架 | [OpenTUI](https://opentui.dev) + SolidJS |
| 包管理 | bun |
| 构建 | Bun.compile（原生二进制） |

## 许可

MIT License
