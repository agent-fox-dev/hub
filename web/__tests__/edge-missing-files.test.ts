/**
 * Edge Case Tests: Missing Project Files (TS-02-E1, TS-02-E9, TS-02-E10)
 *
 * Verifies that npm commands fail with descriptive errors when critical
 * project files are absent: package.json, main.tsx, index.html.
 *
 * Requirements: 02-REQ-1.E1, 02-REQ-6.E1, 02-REQ-6.E2
 */

import { describe, it, expect, afterEach } from "vitest";
import {
  existsSync,
  readFileSync,
  writeFileSync,
  unlinkSync,
  mkdirSync,
} from "node:fs";
import { execSync } from "node:child_process";
import { resolve, join, dirname } from "node:path";

const WEB_DIR = resolve(__dirname, "..");

function runInWebDir(cmd: string): {
  exitCode: number;
  stdout: string;
  stderr: string;
} {
  try {
    const stdout = execSync(cmd, {
      cwd: WEB_DIR,
      encoding: "utf-8",
      stdio: ["pipe", "pipe", "pipe"],
    });
    return { exitCode: 0, stdout, stderr: "" };
  } catch (err: unknown) {
    const e = err as { status?: number; stdout?: string; stderr?: string };
    return {
      exitCode: e.status ?? 1,
      stdout: e.stdout ?? "",
      stderr: e.stderr ?? "",
    };
  }
}

/**
 * Safely back up a file before removing it, and provide a restore function.
 * Returns null if the file does not exist (nothing to back up).
 */
function backupAndRemove(filePath: string): (() => void) | null {
  if (!existsSync(filePath)) return null;
  const content = readFileSync(filePath);
  unlinkSync(filePath);
  return () => {
    const dir = dirname(filePath);
    if (!existsSync(dir)) {
      mkdirSync(dir, { recursive: true });
    }
    writeFileSync(filePath, content);
  };
}

describe("TS-02-E1: npm commands fail when web/package.json is absent", () => {
  let restore: (() => void) | null = null;

  afterEach(() => {
    if (restore) {
      restore();
      restore = null;
    }
  });

  it("npm run build exits with non-zero code when package.json is missing", () => {
    const pkgPath = join(WEB_DIR, "package.json");
    restore = backupAndRemove(pkgPath);
    // If there was nothing to remove, the file is already absent
    expect(existsSync(pkgPath)).toBe(false);

    const result = runInWebDir("npm run build");
    expect(result.exitCode).not.toBe(0);

    const output = (result.stdout + result.stderr).toLowerCase();
    const hasRelevantError =
      output.includes("package.json") ||
      output.includes("no such file") ||
      output.includes("missing") ||
      output.includes("enoent");
    expect(hasRelevantError).toBe(true);
  });
});

describe("TS-02-E9: npm run build fails when web/src/main.tsx is absent", () => {
  let restore: (() => void) | null = null;

  afterEach(() => {
    if (restore) {
      restore();
      restore = null;
    }
  });

  it("npm run build exits with non-zero code and references the missing entry point", () => {
    const mainPath = join(WEB_DIR, "src", "main.tsx");
    restore = backupAndRemove(mainPath);
    expect(existsSync(mainPath)).toBe(false);

    const result = runInWebDir("npm run build");
    expect(result.exitCode).not.toBe(0);

    const output = result.stdout + result.stderr;
    const outputLower = output.toLowerCase();
    const hasRelevantError =
      output.includes("main.tsx") ||
      outputLower.includes("entry") ||
      outputLower.includes("not found") ||
      outputLower.includes("does not exist");
    expect(hasRelevantError).toBe(true);
  });
});

describe("TS-02-E10: npm run build fails when web/index.html is absent", () => {
  let restore: (() => void) | null = null;

  afterEach(() => {
    if (restore) {
      restore();
      restore = null;
    }
  });

  it("npm run build exits with non-zero code and references the missing index.html", () => {
    const indexPath = join(WEB_DIR, "index.html");
    restore = backupAndRemove(indexPath);
    expect(existsSync(indexPath)).toBe(false);

    const result = runInWebDir("npm run build");
    expect(result.exitCode).not.toBe(0);

    const output = result.stdout + result.stderr;
    const outputLower = output.toLowerCase();
    const hasRelevantError =
      output.includes("index.html") ||
      outputLower.includes("entry") ||
      outputLower.includes("not found") ||
      outputLower.includes("does not exist");
    expect(hasRelevantError).toBe(true);
  });
});
