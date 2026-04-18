# 🌸 Banka Code

> *千恋万花，一刀入魂。*

**Banka Code** 是一个基于 TypeScript + [Bun](https://bun.sh) 构建的 Coding Agent，配备千恋万花风格的 TUI 终端界面。

名字来源于 [千恋＊万花](https://www.yuzu-soft.com/products/senren/)（Senren\*Banka）——柚子社十周年纪念作。在这座名为「穗织」的温泉小镇里，万花等待你的指令，然后斩断一切繁琐的编码工作。

## ✨ 特性

- **TypeScript + Bun** — 类型安全与极致性能
- **Coding Agent** — 理解意图、编辑代码、执行命令，一站式完成
- **千恋万花 TUI** — 樱粉暖橘配色、花弁流灯动画、透明终端适配
- **流式输出** — SSE 实时推送，工具调用过程全程可见
- **多 Provider 支持** — OpenAI / Anthropic / Ollama

## 📦 快速开始

```bash
# 安装依赖
bun install

# 配置环境变量
cp .env.example .env

# 启动 TUI
bun run src/index.ts

# 单次执行模式
bun run src/index.ts "帮我看看当前项目结构"
```

### 环境变量

使用 Bun 原生 `.env`，无需额外安装 `dotenv`。

#### OpenAI

```bash
BANKA_PROVIDER=openai
BANKA_API_KEY=your-api-key
BANKA_BASE_URL=https://api.example.com/v1
BANKA_MODEL=your-model-id
```

banka 会向 `${BANKA_BASE_URL}/chat/completions` 发起请求，支持 SSE 流式输出。

#### Anthropic

```bash
BANKA_PROVIDER=anthropic
BANKA_API_KEY=your-api-key
BANKA_BASE_URL=https://api.example.com/anthropic/v1
BANKA_MODEL=your-model-id
```

banka 会向 `${BANKA_BASE_URL}/messages` 发起请求，支持 SSE 流式输出。

#### Ollama（本地）

```bash
BANKA_PROVIDER=ollama
BANKA_BASE_URL=127.0.0.1:11434
BANKA_MODEL=qwen3.5:9b
```

`BANKA_API_KEY` 可以省略。banka 会自动补全为 Ollama 的端点，向 `http://127.0.0.1:11434/v1/chat/completions` 发起请求。

### 可用脚本

```bash
bun run check    # 类型检查
bun test         # 运行测试
```

## 🔧 工具系统

| 工具 | 功能 |
|------|------|
| **Bash** | 执行终端命令 |
| **Read** | 读取文件 |
| **Write** | 写入文件 |
| **Edit** | 局部编辑文件 |
| **Glob** | 按 pattern 查找文件 |
| **Grep** | 按正则搜索文件内容 |

工具在 TUI 中显示人类可读名 + 参数摘要，如 `Bash · ls -la`、`Read · src/tui/app.tsx`。

## 🏗 项目结构

```
banka-code/
├── src/
│   ├── agent/          # agent 主循环（流式 + 多轮对话）
│   ├── errors/         # 自定义错误类型
│   ├── messages/       # 会话消息模型
│   ├── models/         # OpenAI / Anthropic / Ollama 客户端
│   ├── prompt/         # 系统提示词（万花角色设定）
│   ├── runtime/        # 运行时配置
│   ├── shared/         # 共享工具函数
│   ├── tools/          # Bash / Read / Write / Edit / Glob / Grep
│   ├── tui/            # OpenTUI + SolidJS 终端界面
│   └── index.ts        # CLI 入口
├── package.json
├── tsconfig.json
├── .env.example
├── AGENTS.md
└── README.md
```

## 📜 许可

MIT License
