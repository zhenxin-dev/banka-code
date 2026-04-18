/**
 * bwrap 沙箱集成测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { mkdtemp, rm, mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  isBubblewrapAvailable,
  spawnDirect,
  spawnSandboxed
} from "./bwrap-sandbox.ts";

const SPAWN_OPTIONS = { stdout: "pipe" as const, stderr: "pipe" as const, stdin: "ignore" as const };

const hasBwrap: boolean = await isBubblewrapAvailable();

describe("isBubblewrapAvailable", () => {
  it("returns a boolean", async () => {
    const result = await isBubblewrapAvailable();
    expect(typeof result).toBe("boolean");
  });

  it("caches the result", async () => {
    const first = await isBubblewrapAvailable();
    const second = await isBubblewrapAvailable();
    expect(first).toBe(second);
  });
});

describe("spawnDirect", () => {
  it("executes a command and captures stdout", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-direct-test-"));
    try {
      const proc = spawnDirect("echo hello", workspaceRoot, SPAWN_OPTIONS);
      const [stdout, exitCode] = await Promise.all([
        new Response(proc.stdout).text(),
        proc.exited
      ]);
      expect(exitCode).toBe(0);
      expect(stdout.trim()).toBe("hello");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });

  it("captures stderr on failure", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-direct-err-"));
    try {
      const proc = spawnDirect("ls /nonexistent_dir_xyz", workspaceRoot, SPAWN_OPTIONS);
      const [stderr, exitCode] = await Promise.all([
        new Response(proc.stderr).text(),
        proc.exited
      ]);
      expect(exitCode).not.toBe(0);
      expect(stderr).toContain("nonexistent_dir_xyz");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });
});

describe.if(hasBwrap)("spawnSandboxed (bwrap available)", () => {
  it("executes a simple echo command inside the sandbox", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-sandbox-echo-"));
    try {
      const proc = spawnSandboxed("echo sandbox_hello", workspaceRoot, SPAWN_OPTIONS);
      const [stdout, exitCode] = await Promise.all([
        new Response(proc.stdout).text(),
        proc.exited
      ]);
      expect(exitCode).toBe(0);
      expect(stdout.trim()).toBe("sandbox_hello");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });

  it("can read files inside the workspace", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-sandbox-read-"));
    try {
      const testFile = join(workspaceRoot, "test.txt");
      await writeFile(testFile, "workspace content\n");
      const proc = spawnSandboxed("cat test.txt", workspaceRoot, SPAWN_OPTIONS);
      const [stdout, exitCode] = await Promise.all([
        new Response(proc.stdout).text(),
        proc.exited
      ]);
      expect(exitCode).toBe(0);
      expect(stdout).toBe("workspace content\n");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });

  it("cannot access files outside the mounted paths", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-sandbox-isolate-"));
    const outsideDir = await mkdtemp(join(tmpdir(), "banka-outside-"));
    try {
      const secretFile = join(outsideDir, "secret.txt");
      await writeFile(secretFile, "top secret\n");

      const proc = spawnSandboxed(
        `cat ${secretFile}`,
        workspaceRoot,
        SPAWN_OPTIONS
      );
      const [stderr, exitCode] = await Promise.all([
        new Response(proc.stderr).text(),
        proc.exited
      ]);
      expect(exitCode).not.toBe(0);
      expect(stderr.length).toBeGreaterThan(0);
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
      await rm(outsideDir, { recursive: true, force: true });
    }
  });

  it("cannot write to read-only system directories", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-sandbox-ro-"));
    try {
      const proc = spawnSandboxed(
        "touch /usr/banka-test-write-should-fail",
        workspaceRoot,
        SPAWN_OPTIONS
      );
      const [stderr, exitCode] = await Promise.all([
        new Response(proc.stderr).text(),
        proc.exited
      ]);
      expect(exitCode).not.toBe(0);
      expect(stderr.length).toBeGreaterThan(0);
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });
});
