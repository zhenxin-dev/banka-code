# Banka Code 开发指南

## 项目概览

**Banka Code** 是一个使用 TypeScript + Bun 构建的 Coding Agent，配备千恋万花风格的 TUI 终端界面。

- **运行时**：Bun
- **语言**：TypeScript（strict mode）
- **TUI**：OpenTUI + SolidJS
- **包管理**：bun

## 元规则

- 使用中文回复
- 先判断用户意图，再决定是回答、排查还是实施
- 只做用户明确要求的事；不擅自扩展范围
- 先搜索再修改，先验证再结束

## 用户身份

- **用户昵称**：真心
- **@author**：JavaDoc / 文件头注释中使用 `@author 真心`

## 项目命名

项目名 **banka** 来源于 [千恋＊万花](https://www.yuzu-soft.com/products/senren/)（Senren\*Banka）。

相关意象可自由使用：神刀、穗织、温泉小镇、和风、巫女、樱粉、暖橘等。保持灵动、不过度。

## 技术约束

### TypeScript

- strict mode，零 `any`、零 `@ts-ignore`、零 `@ts-expect-error`
- 优先 `interface` 而非 `type`（除非需要联合类型、交叉类型等 `type` 独有能力）
- 导出符号必须有 JSDoc 注释
- 文件头注释格式：

```typescript
/**
 * {模块描述}
 *
 * @author 真心
 */
```

### Bun

- 使用 Bun 原生 API（`Bun.file()`、`Bun.spawn()`、`Bun.Glob` 等），不引入 Node.js polyfill
- 测试使用 `bun test`
- 脚本使用 `bun run` 执行

### 依赖管理

- 添加依赖前先检查项目是否已有同类能力
- 优先已有依赖，一次只更新一个主要依赖
- 记录引入原因（commit message 或注释）

## 意图路由

| 用户表述 | 默认动作 |
|----------|----------|
| 「解释 / 分析 / 对比 / 怎么做」 | 只研究和回答，不修改文件 |
| 「查一下 / 看看 / 排查 / 定位」 | 先搜索代码、配置、日志，再给结论 |
| 「实现 / 添加 / 修改 / 修复」 | 先找现有模式和边界，再做最小实现 |
| 「重构 / 优化 / 清理」 | 先评估收益、风险、影响范围，再分步执行 |
| 需求不清楚 | 先探索；仍不清楚时只问一个最小必要问题 |

## 代码风格

### 命名

- 文件：`kebab-case`（`agent-runner.ts`）
- 类 / 接口：`PascalCase`（`ToolExecutor`）
- 函数 / 变量：`camelCase`（`parseCommand`）
- 常量：`UPPER_SNAKE_CASE`（`MAX_RETRIES`）
- 类型参数：`PascalCase`，单个字母仅用于简单泛型（`T`、`K`、`V`）
- **工具内部名**：`PascalCase`（`Bash` / `Read` / `Write` / `Edit` / `Glob` / `Grep`）

### 结构

- 一个文件一个职责
- 公共 API 通过 `index.ts` 统一导出
- 错误处理使用自定义 `Error` 子类，不抛裸字符串
- 异步操作使用 `async/await`，不用 `.then()` 链

### 注释

- 解释「为什么」，不重复「做了什么」
- `TODO` 必须关联上下文（issue 编号或简要说明）
- 公共 API 必须有 JSDoc

### TUI / 视觉

- 配色使用千恋万花主题（樱粉、暖橘、淡金、赤褐）
- 适配透明终端背景：边框和文字用柔和高对比色，避免厚重实底
- 工具调用在 TUI 中显示人类可读名 + 参数摘要（如 `Bash · ls -la`、`Read · src/tui/app.tsx`）

## Provider 支持

| Provider | 环境变量 `BANKA_PROVIDER` | 需要 API Key | 说明 |
|----------|--------------------------|-------------|------|
| OpenAI 兼容 | `openai-compatible` | 是 | 默认 |
| Anthropic 兼容 | `anthropic-compatible` | 是 | |
| Ollama | `ollama` | 否 | 自动补全 base URL |
| Mock | `mock`（自动回退） | 否 | 无 `.env` 时使用 |

## 工具系统

| 工具名 | 功能 | 源文件 |
|--------|------|--------|
| `Bash` | 执行终端命令 | `src/tools/bash-tool.ts` |
| `Read` | 读取文件 | `src/tools/file-tools.ts` |
| `Write` | 写入文件 | `src/tools/file-tools.ts` |
| `Edit` | 局部编辑文件 | `src/tools/file-tools.ts` |
| `Glob` | 按 pattern 查找文件 | `src/tools/glob-tool.ts` |
| `Grep` | 按正则搜索文件内容 | `src/tools/grep-tool.ts` |

## 执行协议

### 1. 理解

开始前先明确：目标、约束、影响范围、验证方式。

### 2. 探索

- 先找 2-3 个类似实现，确认现有模式
- 先看入口、调用链、配置和测试，再动代码
- 仓库是第一事实来源；未读内容不做确定性判断

### 3. 决策

- 优先复用已有实现、已有依赖、已有工具
- 优先选择「无聊但可靠」的方案
- 超过单文件、跨层、或范围不明的修改，先给简要计划再动手

### 4. 实施

- 小步修改，保持每一步可验证、可回滚
- Bugfix 只修问题本身，不顺带重构
- 非必要不新建文件、不引入新依赖、不改公共接口

### 5. 验证

- `bun run check` 类型检查通过
- `bun test` 测试通过
- 新功能有对应测试
- `lsp_diagnostics` 无新增错误

### 6. 交付

说明：改了什么、改在哪、如何验证、是否存在未处理风险。

## 硬约束

| 规则 | 说明 |
|------|------|
| 类型安全 | 禁止 `any`、`@ts-ignore`、`@ts-expect-error`、空 `catch` |
| 需求边界 | 禁止擅自扩大需求、追加功能、顺手重构 |
| 代码猜测 | 禁止对未读代码、未跑结果、未看文档的内容做确定性判断 |
| 验证造假 | 未执行命令、未跑测试、未看输出，不得声称「已验证」 |
| 卡点次数 | 同一问题最多尝试 3 次；超过后停止并说明现状 |

## 测试

- 文件命名：`{模块名}.test.ts`，与源文件同目录或放 `__tests__/`
- 核心业务、边界、异常必须覆盖
- 测试互不依赖，数据自包含

## Git 提交

遵循 [Conventional Commits](https://www.conventionalcommits.org/)，**提交信息使用中文**：

```
type(scope): 简要描述

详细说明（可选）
```

类型：`feat` / `fix` / `refactor` / `docs` / `test` / `chore` / `perf`

如用户要求提交，末尾添加：

```
Co-Authored-By: opencode <noreply@opencode.ai>
```

## 完成定义

- 用户请求的范围已覆盖
- 修改与现有代码风格一致
- 类型检查 / 测试 / 构建已通过
- 功能已实际验证，有对应证据
