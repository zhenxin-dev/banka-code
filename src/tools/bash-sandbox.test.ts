/**
 * Bash 命令安全校验测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { validateCommand } from "./bash-sandbox.ts";

const WORKSPACE = "/tmp/banka-sandbox-test";

describe("validateCommand — allowed commands", () => {
  it("allows echo hello", () => {
    expect(validateCommand("echo hello", WORKSPACE)).toBeNull();
  });

  it("allows bun test", () => {
    expect(validateCommand("bun test", WORKSPACE)).toBeNull();
  });

  it("allows git status", () => {
    expect(validateCommand("git status", WORKSPACE)).toBeNull();
  });

  it("allows ls with relative path", () => {
    expect(validateCommand("ls src/", WORKSPACE)).toBeNull();
  });

  it("allows cat with workspace-relative file", () => {
    expect(validateCommand("cat README.md", WORKSPACE)).toBeNull();
  });

  it("allows chmod on workspace file", () => {
    expect(validateCommand("chmod +x build.sh", WORKSPACE)).toBeNull();
  });

  it("allows redirect to relative path within workspace", () => {
    expect(validateCommand("echo test > output.txt", WORKSPACE)).toBeNull();
  });

  it("allows append redirect to relative path", () => {
    expect(validateCommand("echo test >> output.txt", WORKSPACE)).toBeNull();
  });

  it("allows export of non-dangerous variable", () => {
    expect(validateCommand("export FOO=bar", WORKSPACE)).toBeNull();
  });

  it("allows empty command", () => {
    expect(validateCommand("", WORKSPACE)).toBeNull();
  });

  it("allows whitespace-only command", () => {
    expect(validateCommand("   ", WORKSPACE)).toBeNull();
  });

  it("allows piped commands within workspace", () => {
    expect(validateCommand("echo hello | grep hello", WORKSPACE)).toBeNull();
  });
});

describe("validateCommand — rejected absolute paths", () => {
  it("rejects ls /etc", () => {
    const result = validateCommand("ls /etc", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("/etc");
  });

  it("rejects cat /etc/passwd", () => {
    const result = validateCommand("cat /etc/passwd", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("/etc/passwd");
  });

  it("rejects rm -rf /", () => {
    const result = validateCommand("rm -rf /", WORKSPACE);
    expect(result).not.toBeNull();
  });

  it("rejects Windows-style absolute path", () => {
    const result = validateCommand("cat C:\\Windows\\System32\\config", WORKSPACE);
    expect(result).not.toBeNull();
  });

  it("rejects ls /tmp/exploit", () => {
    const result = validateCommand("ls /tmp/exploit", WORKSPACE);
    expect(result).not.toBeNull();
  });
});

describe("validateCommand — rejected path escape", () => {
  it("rejects cat ../../secret", () => {
    const result = validateCommand("cat ../../secret", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("../../secret");
  });

  it("rejects ls ../outside", () => {
    const result = validateCommand("ls ../outside", WORKSPACE);
    expect(result).not.toBeNull();
  });

  it("rejects deeply nested escape", () => {
    const result = validateCommand("cat ../../../etc/passwd", WORKSPACE);
    expect(result).not.toBeNull();
  });
});

describe("validateCommand — rejected cd outside", () => {
  it("rejects cd /tmp", () => {
    const result = validateCommand("cd /tmp", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("/tmp");
  });

  it("rejects cd ../.. when escaping workspace", () => {
    const result = validateCommand("cd ../..", WORKSPACE);
    expect(result).not.toBeNull();
  });

  it("rejects cd to system path", () => {
    const result = validateCommand("cd /etc", WORKSPACE);
    expect(result).not.toBeNull();
  });
});

describe("validateCommand — rejected redirects", () => {
  it("rejects redirect to /etc/passwd", () => {
    const result = validateCommand("echo x > /etc/passwd", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("/etc/passwd");
  });

  it("rejects append redirect to /tmp/exploit", () => {
    const result = validateCommand("echo x >> /tmp/exploit", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("/tmp/exploit");
  });
});

describe("validateCommand — rejected env manipulation", () => {
  it("rejects export PATH=/evil", () => {
    const result = validateCommand("export PATH=/evil", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("PATH");
  });

  it("rejects export LD_PRELOAD", () => {
    const result = validateCommand("export LD_PRELOAD=/evil.so", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("LD_PRELOAD");
  });

  it("rejects export HOME override", () => {
    const result = validateCommand("export HOME=/evil", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("HOME");
  });

  it("rejects inline PATH assignment", () => {
    const result = validateCommand("PATH=/evil cmd", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("PATH");
  });
});

describe("validateCommand — rejected privilege escalation", () => {
  it("rejects sudo rm -rf /", () => {
    const result = validateCommand("sudo rm -rf /", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("sudo");
  });

  it("rejects su root", () => {
    const result = validateCommand("su root", WORKSPACE);
    expect(result).not.toBeNull();
    expect(result).toContain("su");
  });

  it("rejects sudo as substring in word boundary", () => {
    const result = validateCommand("sudo ls", WORKSPACE);
    expect(result).not.toBeNull();
  });
});
