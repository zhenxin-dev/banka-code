#!/usr/bin/env bun

/**
 * Banka Code 原生二进制构建脚本。
 *
 * @author 真心
 */

import { $ } from "bun"
import fs from "fs"
import path from "path"
import { fileURLToPath } from "url"
import { createSolidTransformPlugin } from "@opentui/solid/bun-plugin"

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const dir = path.resolve(__dirname, "..")

process.chdir(dir)

const pkg = await Bun.file("./package.json").json()

const singleFlag = process.argv.includes("--single")
const skipInstall = process.argv.includes("--skip-install")
const plugin = createSolidTransformPlugin()

const allTargets: {
  os: string
  arch: "arm64" | "x64"
}[] = [
  { os: "linux", arch: "x64" },
  { os: "linux", arch: "arm64" },
  { os: "darwin", arch: "arm64" },
  { os: "darwin", arch: "x64" },
  { os: "win32", arch: "x64" },
  { os: "win32", arch: "arm64" },
]

const targets = singleFlag
  ? allTargets.filter(
      (item) => item.os === process.platform && item.arch === process.arch,
    )
  : allTargets

await $`rm -rf dist`

if (!skipInstall) {
  await $`bun install --os="*" --cpu="*" @opentui/core@${pkg.dependencies["@opentui/core"]}`.quiet()
}

const localWorkerPath = path.resolve(dir, "node_modules/@opentui/core/parser.worker.js")
const rootWorkerPath = path.resolve(dir, "../../node_modules/@opentui/core/parser.worker.js")
const parserWorker = fs.realpathSync(fs.existsSync(localWorkerPath) ? localWorkerPath : rootWorkerPath)

const binaryName = "banka"

for (const item of targets) {
  const targetName = [
    pkg.name,
    item.os === "win32" ? "windows" : item.os,
    item.arch,
  ]
    .filter(Boolean)
    .join("-")

  const bunTarget = [
    "bun",
    item.os === "win32" ? "windows" : item.os,
    item.arch,
  ].join("-")

  await $`mkdir -p dist/${targetName}/bin`.quiet()

  const bunfsRoot = item.os === "win32" ? "B:/~BUN/root/" : "/$bunfs/root/"
  const workerRelativePath = path.relative(dir, parserWorker).split("\\").join("/")
  const ext = item.os === "win32" ? ".exe" : ""

  const result = await Bun.build({
    conditions: ["browser"],
    tsconfig: "./tsconfig.json",
    plugins: [plugin],
    external: ["node-gyp"],
    format: "esm",
    minify: true,
    splitting: true,
    compile: {
      autoloadBunfig: false,
      autoloadDotenv: false,
      autoloadTsconfig: true,
      autoloadPackageJson: true,
      target: bunTarget as never,
      outfile: `dist/${targetName}/bin/${binaryName}${ext}`,
      execArgv: [`--user-agent=banka-code/${pkg.version}`, "--"],
      windows: {},
    },
    entrypoints: ["./src/index.ts", parserWorker],
    define: {
      BANKA_CODE_VERSION: `'${pkg.version}'`,
      OTUI_TREE_SITTER_WORKER_PATH: bunfsRoot + workerRelativePath,
    },
  })

  if (!result.success) {
    for (const log of result.logs) {
      console.error(log)
    }
    process.exit(1)
  }

  if (item.os === process.platform && item.arch === process.arch) {
    const binaryPath = path.resolve(dir, `dist/${targetName}/bin/${binaryName}${ext}`)
    try {
      const versionOutput = await $`${binaryPath} --version`.quiet().text()
      if (!versionOutput.includes(pkg.version)) {
        console.error(`版本不匹配: ${versionOutput.trim()} !== ${pkg.version}`)
        process.exit(1)
      }
    } catch (e) {
      console.error(`冒烟测试失败: ${e}`)
      process.exit(1)
    }
  }

  await Bun.file(`dist/${targetName}/package.json`).write(
    JSON.stringify(
      {
        name: targetName,
        version: pkg.version,
        os: [item.os],
        cpu: [item.arch],
      },
      null,
      2,
    ),
  )

  console.log(`✓ ${targetName}`)
}

console.log("")
for (const item of targets) {
  const targetName = [
    pkg.name,
    item.os === "win32" ? "windows" : item.os,
    item.arch,
  ]
    .filter(Boolean)
    .join("-")
  const ext = item.os === "win32" ? ".exe" : ""
  const binaryPath = `dist/${targetName}/bin/${binaryName}${ext}`
  if (fs.existsSync(binaryPath)) {
    const stat = fs.statSync(binaryPath)
    console.log(`  ${binaryPath} (${(stat.size / 1024 / 1024).toFixed(1)} MB)`)
  }
}
