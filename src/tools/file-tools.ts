/**
 * 文件工具实现。
 *
 * @author 真心
 */

import { mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { ToolExecutionError } from "../errors/banka-error.ts";
import { resolveSafePath } from "./safe-path.ts";
import type { ToolArguments, ToolDefinition, ToolExecutionContext, ToolResult } from "./tool.ts";

const MAX_READ_FILE_BYTES = 1_000_000;

interface ReadFileInput {
  readonly path: string;
}

interface WriteFileInput {
  readonly path: string;
  readonly content: string;
}

interface EditFileInput {
  readonly path: string;
  readonly oldText: string;
  readonly newText: string;
}

/**
 * 创建 Read 工具定义。
 */
export function createReadFileTool(): ToolDefinition {
  return {
    name: "Read",
    description: "Read a text file from the current workspace.",
    inputSchema: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Path to the file relative to the workspace root."
        }
      },
      required: ["path"],
      additionalProperties: false
    },
    async execute(arguments_: ToolArguments, context: ToolExecutionContext): Promise<ToolResult> {
      const input = parseReadFileInput(arguments_);
      const absolutePath = resolveSafePath(context.workspaceRoot, input.path);
      const file = Bun.file(absolutePath);

      if (!(await file.exists())) {
        throw new ToolExecutionError(`File does not exist: ${input.path}`);
      }

      if (file.size > MAX_READ_FILE_BYTES) {
        throw new ToolExecutionError(
          `File is too large to read safely: ${input.path} (${file.size} bytes).`
        );
      }

      return {
        content: await file.text(),
        isError: false
      };
    }
  };
}

/**
 * 创建 Write 工具定义。
 */
export function createWriteFileTool(): ToolDefinition {
  return {
    name: "Write",
    description: "Create or overwrite a text file in the current workspace.",
    inputSchema: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Path to the file relative to the workspace root."
        },
        content: {
          type: "string",
          description: "Full text content to write."
        }
      },
      required: ["path", "content"],
      additionalProperties: false
    },
    async execute(arguments_: ToolArguments, context: ToolExecutionContext): Promise<ToolResult> {
      const input = parseWriteFileInput(arguments_);
      const absolutePath = resolveSafePath(context.workspaceRoot, input.path);

      await mkdir(dirname(absolutePath), { recursive: true });
      await Bun.write(absolutePath, input.content);

      return {
        content: `Wrote file: ${input.path}`,
        isError: false
      };
    }
  };
}

/**
 * 创建 Edit 工具定义。
 */
export function createEditFileTool(): ToolDefinition {
  return {
    name: "Edit",
    description: "Replace a unique text fragment inside a file.",
    inputSchema: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Path to the file relative to the workspace root."
        },
        oldText: {
          type: "string",
          description: "Existing text that must appear exactly once."
        },
        newText: {
          type: "string",
          description: "Replacement text."
        }
      },
      required: ["path", "oldText", "newText"],
      additionalProperties: false
    },
    async execute(arguments_: ToolArguments, context: ToolExecutionContext): Promise<ToolResult> {
      const input = parseEditFileInput(arguments_);
      const absolutePath = resolveSafePath(context.workspaceRoot, input.path);
      const file = Bun.file(absolutePath);

      if (!(await file.exists())) {
        throw new ToolExecutionError(`File does not exist: ${input.path}`);
      }

      const currentContent = await file.text();
      const matchCount = countOccurrences(currentContent, input.oldText);

      if (matchCount === 0) {
        throw new ToolExecutionError(`Target text was not found in: ${input.path}`);
      }

      if (matchCount > 1) {
        throw new ToolExecutionError(`Target text must appear exactly once in: ${input.path}`);
      }

      const nextContent = currentContent.replace(input.oldText, () => input.newText);
      await Bun.write(absolutePath, nextContent);

      return {
        content: `Edited file: ${input.path}`,
        isError: false
      };
    }
  };
}

function parseReadFileInput(arguments_: ToolArguments): ReadFileInput {
  const path = arguments_["path"];

  if (typeof path !== "string" || path.trim() === "") {
    throw new ToolExecutionError("Read tool requires a non-empty 'path' string.");
  }

  return { path };
}

function parseWriteFileInput(arguments_: ToolArguments): WriteFileInput {
  const path = arguments_["path"];
  const content = arguments_["content"];

  if (typeof path !== "string" || path.trim() === "") {
    throw new ToolExecutionError("Write tool requires a non-empty 'path' string.");
  }

  if (typeof content !== "string") {
    throw new ToolExecutionError("Write tool requires a string 'content' field.");
  }

  return { path, content };
}

function parseEditFileInput(arguments_: ToolArguments): EditFileInput {
  const path = arguments_["path"];
  const oldText = arguments_["oldText"];
  const newText = arguments_["newText"];

  if (typeof path !== "string" || path.trim() === "") {
    throw new ToolExecutionError("Edit tool requires a non-empty 'path' string.");
  }

  if (typeof oldText !== "string" || oldText === "") {
    throw new ToolExecutionError("Edit tool requires a non-empty 'oldText' string.");
  }

  if (typeof newText !== "string") {
    throw new ToolExecutionError("Edit tool requires a string 'newText' field.");
  }

  return { path, oldText, newText };
}

function countOccurrences(content: string, target: string): number {
  let count = 0;
  let searchIndex = 0;

  while (searchIndex < content.length) {
    const matchIndex = content.indexOf(target, searchIndex);

    if (matchIndex === -1) {
      return count;
    }

    count += 1;
    searchIndex = matchIndex + target.length;
  }

  return count;
}
