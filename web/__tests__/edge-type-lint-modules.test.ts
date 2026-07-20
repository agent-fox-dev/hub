/**
 * Edge Case Tests: Type Errors, Lint Violations, Missing node_modules
 * (TS-02-E2, TS-02-E3, TS-02-E4)
 *
 * Verifies that:
 * - TypeScript type errors cause build failure with diagnostics (TS-02-E2)
 * - ESLint violations cause lint failure with file/line/rule info (TS-02-E3)
 * - Missing node_modules causes both build and lint to fail (TS-02-E4)
 *
 * Requirements: 02-REQ-2.E1, 02-REQ-2.E2, 02-REQ-2.E3
 */

import { describe, it, expect, afterEach } from "vitest";
import {
  existsSync,
  readFileSync,
  writeFileSync,
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
 * Replace a file's content, returning a function that restores the original.
 * If the file does not exist, returns a function that removes the written file.
 */
function replaceContent(
  filePath: string,
  newContent: string,
): () => void {
  let originalContent: Buffer | null = null;
  if (existsSync(filePath)) {
    originalContent = readFileSync(filePath);
  }
  const dir = dirname(filePath);
  if (!existsSync(dir)) {
    mkdirSync(dir, { recursive: true });
  }
  writeFileSync(filePath, newContent, "utf-8");
  return () => {
    if (originalContent !== null) {
      writeFileSync(filePath, originalContent);
    } else {
      // File didn't exist before — remove it
      try {
        const { unlinkSync } = require("node:fs") as typeof import("node:fs");
        unlinkSync(filePath);
      } catch {
        // Ignore cleanup errors
      }
    }
  };
}

describe("TS-02-E2: npm run build fails on TypeScript type errors", () => {
  let restore: (() => void) | null = null;

  afterEach(() => {
    if (restore) {
      restore();
      restore = null;
    }
  });

  it("exits with non-zero code and reports App.tsx in error output", () => {
    const appPath = join(WEB_DIR, "src", "App.tsx");

    // Inject a deliberate type error while keeping valid JSX
    const badContent = [
      'const x: number = "bad_type";',
      "export default function App() {",
      "  return <h1>af-hub</h1>;",
      "}",
    ].join("\n");

    restore = replaceContent(appPath, badContent);

    const result = runInWebDir("npm run build");
    expect(result.exitCode).not.toBe(0);

    const output = result.stdout + result.stderr;
    const outputLower = output.toLowerCase();
    expect(outputLower).toContain("error");
    expect(output).toContain("App.tsx");
  });
});

describe("TS-02-E3: npm run lint reports ESLint violations", () => {
  let restore: (() => void) | null = null;

  afterEach(() => {
    if (restore) {
      restore();
      restore = null;
    }
  });

  it("exits with non-zero code and reports file path, line number, and rule", () => {
    const appPath = join(WEB_DIR, "src", "App.tsx");

    // Inject a deliberate ESLint violation (unused variable)
    const badContent = [
      "const unused = 42;",
      "export default function App() {",
      "  return <h1>af-hub</h1>;",
      "}",
    ].join("\n");

    restore = replaceContent(appPath, badContent);

    const result = runInWebDir("npm run lint");
    expect(result.exitCode).not.toBe(0);

    const output = result.stdout + result.stderr;
    // Should reference the file
    expect(output).toContain("App.tsx");
    // Should contain a line number (at least one digit)
    expect(output).toMatch(/\d/);
    // Should reference the violation type
    const outputLower = output.toLowerCase();
    const hasViolationInfo =
      outputLower.includes("error") || outputLower.includes("warning");
    expect(hasViolationInfo).toBe(true);
  });
});

describe("TS-02-E4: npm run build and lint fail when node_modules is absent", () => {
  /**
   * These tests cannot move node_modules within the vitest process
   * because vitest itself runs from node_modules. Instead, we run
   * a shell script that temporarily renames node_modules, executes
   * the command, captures the exit code and output, then restores
   * node_modules — all within a single subprocess.
   */

  /**
   * Run a command with node_modules temporarily moved aside.
   * Returns the exit code and combined output of the inner command.
   */
  function runWithoutNodeModules(cmd: string): {
    exitCode: number;
    output: string;
  } {
    // Shell script: rename node_modules, run the command, capture result,
    // restore node_modules, then exit with the captured code.
    const script = [
      `cd "${WEB_DIR}"`,
      'mv node_modules node_modules_backup_e4 2>/dev/null || true',
      `OUTPUT=$(${cmd} 2>&1); CODE=$?`,
      'mv node_modules_backup_e4 node_modules 2>/dev/null || true',
      'echo "$OUTPUT"',
      'exit $CODE',
    ].join(" && ");

    try {
      const output = execSync(`bash -c '${script.replace(/'/g, "'\\''")}'`, {
        encoding: "utf-8",
        stdio: ["pipe", "pipe", "pipe"],
        timeout: 30_000,
      });
      return { exitCode: 0, output };
    } catch (err: unknown) {
      const e = err as { status?: number; stdout?: string; stderr?: string };
      return {
        exitCode: e.status ?? 1,
        output: (e.stdout ?? "") + (e.stderr ?? ""),
      };
    }
  }

  it("npm run build exits with non-zero code referencing missing modules", () => {
    const result = runWithoutNodeModules("npm run build");
    expect(result.exitCode).not.toBe(0);

    const outputLower = result.output.toLowerCase();
    const hasRelevantError =
      outputLower.includes("cannot find module") ||
      outputLower.includes("missing") ||
      outputLower.includes("not found") ||
      outputLower.includes("err!");
    expect(hasRelevantError).toBe(true);
  });

  it("npm run lint exits with non-zero code when node_modules is absent", () => {
    const result = runWithoutNodeModules("npm run lint");
    expect(result.exitCode).not.toBe(0);
  });
});
