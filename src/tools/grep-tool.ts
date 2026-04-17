/**
 * Grep 工具实现。
 *
 * @author 真心
 */

import { stat } from "node:fs/promises";
import { join, relative } from "node:path";
import { ToolExecutionError } from "../errors/banka-error.ts";
import { resolveSafePath } from "./safe-path.ts";
import type { ToolArguments, ToolDefinition, ToolExecutionContext, ToolResult } from "./tool.ts";

interface GrepToolInput {
  readonly pattern: string;
  readonly path?: string;
  readonly include?: string;
  readonly outputMode?: GrepOutputMode;
}

type GrepOutputMode = "content" | "files_with_matches";

const DEFAULT_GREP_OUTPUT_MODE: GrepOutputMode = "content";
const MAX_GREP_RESULTS = 200;
const MAX_GREP_FILE_BYTES = 1_000_000;

/**
 * 创建 Grep 工具定义。
 */
export function createGrepTool(): ToolDefinition {
  return {
    name: "Grep",
    description: "Search file contents by regular expression inside the current workspace.",
    inputSchema: {
      type: "object",
      properties: {
        pattern: {
          type: "string",
          description: "Regular expression pattern to search for."
        },
        path: {
          type: "string",
          description: "Directory or file to search in, relative to the workspace root. Defaults to workspace root."
        },
        include: {
          type: "string",
          description: "Glob pattern used to filter searched files, such as '*.ts'. Defaults to '**/*'."
        },
        outputMode: {
          type: "string",
          description: "Output mode: 'content' or 'files_with_matches'. Defaults to 'content'."
        }
      },
      required: ["pattern"],
      additionalProperties: false
    },
    async execute(arguments_: ToolArguments, context: ToolExecutionContext): Promise<ToolResult> {
      const input = parseGrepToolInput(arguments_);
      const searchRoot = input.path === undefined
        ? context.workspaceRoot
        : resolveSafePath(context.workspaceRoot, input.path);
      const searchRootStat = await readStat(searchRoot, input.path ?? ".");
      const matcher = compilePattern(input.pattern);
      const results: string[] = [];
      const outputMode = input.outputMode ?? DEFAULT_GREP_OUTPUT_MODE;

      if (searchRootStat.isDirectory()) {
        const includePattern = input.include ?? "**/*";
        const glob = new Bun.Glob(includePattern);

        for await (const matchPath of glob.scan({ cwd: searchRoot, onlyFiles: true })) {
          const absolutePath = join(searchRoot, matchPath);
          const nextResults = await collectMatches(absolutePath, context.workspaceRoot, matcher, outputMode);
          appendResults(results, nextResults);

          if (results.length >= MAX_GREP_RESULTS) {
            return {
              content: `${results.slice(0, MAX_GREP_RESULTS).join("\n")}\n\n[truncated]`,
              isError: false
            };
          }
        }
      } else if (searchRootStat.isFile()) {
        const nextResults = await collectMatches(searchRoot, context.workspaceRoot, matcher, outputMode);
        appendResults(results, nextResults);
      } else {
        throw new ToolExecutionError(`Grep tool requires a file or directory path: ${input.path ?? "."}`);
      }

      if (results.length === 0) {
        return {
          content: "No matches found.",
          isError: false
        };
      }

      return {
        content: results.join("\n"),
        isError: false
      };
    }
  };
}

function parseGrepToolInput(arguments_: ToolArguments): GrepToolInput {
  const pattern = arguments_["pattern"];
  const path = arguments_["path"];
  const include = arguments_["include"];
  const outputMode = arguments_["outputMode"];

  if (typeof pattern !== "string" || pattern.trim() === "") {
    throw new ToolExecutionError("Grep tool requires a non-empty 'pattern' string.");
  }

  if (path !== undefined && (typeof path !== "string" || path.trim() === "")) {
    throw new ToolExecutionError("Grep tool requires 'path' to be a non-empty string when provided.");
  }

  if (include !== undefined && (typeof include !== "string" || include.trim() === "")) {
    throw new ToolExecutionError("Grep tool requires 'include' to be a non-empty string when provided.");
  }

  if (
    outputMode !== undefined
    && outputMode !== "content"
    && outputMode !== "files_with_matches"
  ) {
    throw new ToolExecutionError("Grep tool requires 'outputMode' to be 'content' or 'files_with_matches'.");
  }

  return {
    pattern,
    ...(path === undefined ? {} : { path }),
    ...(include === undefined ? {} : { include }),
    ...(outputMode === undefined ? {} : { outputMode })
  };
}

function compilePattern(pattern: string): RegExp {
  try {
    return new RegExp(pattern);
  } catch (error) {
    throw new ToolExecutionError(`Invalid grep pattern: ${pattern}`, { cause: error });
  }
}

async function collectMatches(
  absolutePath: string,
  workspaceRoot: string,
  matcher: RegExp,
  outputMode: GrepOutputMode
): Promise<readonly string[]> {
  const file = Bun.file(absolutePath);

  if (file.size > MAX_GREP_FILE_BYTES) {
    return [];
  }

  const content = await file.text();

  if (content.includes("\u0000")) {
    return [];
  }

  const relativePath = relative(workspaceRoot, absolutePath);
  const lines = content.split(/\r?\n/);

  if (outputMode === "files_with_matches") {
    for (const line of lines) {
      if (matchesLine(matcher, line)) {
        return [relativePath];
      }
    }

    return [];
  }

  const results: string[] = [];

  lines.forEach((line, index) => {
    if (matchesLine(matcher, line)) {
      results.push(`${relativePath}:${index + 1}: ${line}`);
    }
  });

  return results;
}

function matchesLine(matcher: RegExp, line: string): boolean {
  matcher.lastIndex = 0;
  return matcher.test(line);
}

function appendResults(results: string[], nextResults: readonly string[]): void {
  nextResults.forEach((result) => {
    if (!results.includes(result)) {
      results.push(result);
    }
  });
}

async function readStat(targetPath: string, displayPath: string): Promise<Awaited<ReturnType<typeof stat>>> {
  try {
    return await stat(targetPath);
  } catch (error) {
    throw new ToolExecutionError(`Path does not exist: ${displayPath}`, { cause: error });
  }
}
