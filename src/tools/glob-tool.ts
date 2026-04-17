/**
 * Glob 工具实现。
 *
 * @author 真心
 */

import { stat } from "node:fs/promises";
import { join, relative } from "node:path";
import { ToolExecutionError } from "../errors/banka-error.ts";
import { resolveSafePath } from "./safe-path.ts";
import type { ToolArguments, ToolDefinition, ToolExecutionContext, ToolResult } from "./tool.ts";

interface GlobToolInput {
  readonly pattern: string;
  readonly path?: string;
}

const MAX_GLOB_RESULTS = 100;

/**
 * 创建 Glob 工具定义。
 */
export function createGlobTool(): ToolDefinition {
  return {
    name: "Glob",
    description: "Find files by glob pattern inside the current workspace.",
    inputSchema: {
      type: "object",
      properties: {
        pattern: {
          type: "string",
          description: "Glob pattern to match files, such as '**/*.ts'."
        },
        path: {
          type: "string",
          description: "Directory to search in, relative to the workspace root. Defaults to workspace root."
        }
      },
      required: ["pattern"],
      additionalProperties: false
    },
    async execute(arguments_: ToolArguments, context: ToolExecutionContext): Promise<ToolResult> {
      const input = parseGlobToolInput(arguments_);
      const searchRoot = input.path === undefined
        ? context.workspaceRoot
        : resolveSafePath(context.workspaceRoot, input.path);

      const searchRootStat = await readStat(searchRoot, input.path ?? ".");

      if (!searchRootStat.isDirectory()) {
        throw new ToolExecutionError(`Glob tool requires a directory path: ${input.path ?? "."}`);
      }

      const glob = new Bun.Glob(input.pattern);
      const matches: string[] = [];
      let truncated = false;

      for await (const matchPath of glob.scan({ cwd: searchRoot, onlyFiles: true })) {
        matches.push(relative(context.workspaceRoot, join(searchRoot, matchPath)));

        if (matches.length >= MAX_GLOB_RESULTS) {
          truncated = true;
          break;
        }
      }

      if (matches.length === 0) {
        return {
          content: "No files matched the pattern.",
          isError: false
        };
      }

      return {
        content: truncated ? `${matches.join("\n")}\n\n[truncated]` : matches.join("\n"),
        isError: false
      };
    }
  };
}

function parseGlobToolInput(arguments_: ToolArguments): GlobToolInput {
  const pattern = arguments_["pattern"];
  const path = arguments_["path"];

  if (typeof pattern !== "string" || pattern.trim() === "") {
    throw new ToolExecutionError("Glob tool requires a non-empty 'pattern' string.");
  }

  if (path !== undefined && (typeof path !== "string" || path.trim() === "")) {
    throw new ToolExecutionError("Glob tool requires 'path' to be a non-empty string when provided.");
  }

  return {
    pattern,
    ...(path === undefined ? {} : { path })
  };
}

async function readStat(targetPath: string, displayPath: string): Promise<Awaited<ReturnType<typeof stat>>> {
  try {
    return await stat(targetPath);
  } catch (error) {
    throw new ToolExecutionError(`Path does not exist: ${displayPath}`, { cause: error });
  }
}
