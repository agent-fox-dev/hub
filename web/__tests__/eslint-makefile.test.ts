/**
 * ESLint Configuration and Makefile Target Tests (TS-02-14 through TS-02-18)
 *
 * Verifies that ESLint is configured with @typescript-eslint/recommended,
 * and that the root Makefile targets web-dev, web-build, and web-lint
 * function correctly.
 *
 * Requirements: 02-REQ-4.1, 02-REQ-4.2, 02-REQ-5.1, 02-REQ-5.2, 02-REQ-5.3
 */

import { describe, it, expect, afterAll } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { resolve, join } from "node:path";
import { execSync, ChildProcess, spawn } from "node:child_process";

const WEB_DIR = resolve(__dirname, "..");
const REPO_ROOT = resolve(WEB_DIR, "..");

/**
 * Find the ESLint config file in web/.
 */
function findEslintConfig(): string | null {
  const candidates = [
    "eslint.config.js",
    "eslint.config.ts",
    "eslint.config.mjs",
    ".eslintrc.cjs",
    ".eslintrc.js",
    ".eslintrc.json",
  ];
  for (const name of candidates) {
    const fullPath = join(WEB_DIR, name);
    if (existsSync(fullPath)) return fullPath;
  }
  return null;
}

function runCommand(
  cmd: string,
  cwd: string,
): { exitCode: number; output: string } {
  try {
    const output = execSync(cmd, {
      cwd,
      encoding: "utf-8",
      stdio: ["pipe", "pipe", "pipe"],
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

describe("TS-02-14: ESLint config extends @typescript-eslint/recommended", () => {
  it("ESLint config file references typescript-eslint recommended rules", () => {
    const configPath = findEslintConfig();
    expect(configPath).not.toBeNull();
    const content = readFileSync(configPath!, "utf-8");
    // Config may reference it as '@typescript-eslint/recommended' or
    // via the 'typescript-eslint' flat config package
    const hasRef =
      content.includes("@typescript-eslint/recommended") ||
      content.includes("typescript-eslint") ||
      content.includes("tseslint");
    expect(hasRef).toBe(true);
  });
});

describe("TS-02-15: ESLint config file exists in web/", () => {
  it("at least one ESLint config file format exists", () => {
    const configPath = findEslintConfig();
    expect(configPath).not.toBeNull();
  });
});

describe("TS-02-16: make web-dev starts the Vite dev server", () => {
  let devProc: ChildProcess | null = null;

  afterAll(() => {
    if (devProc) {
      devProc.kill("SIGTERM");
      devProc = null;
    }
  });

  it("make web-dev starts and prints the dev server URL within 15 seconds", async () => {
    const started = await new Promise<{
      process: ChildProcess;
      output: string;
    }>((resolve, reject) => {
      const proc = spawn("make", ["web-dev"], {
        cwd: REPO_ROOT,
        stdio: ["pipe", "pipe", "pipe"],
      });

      let output = "";
      const timeout = setTimeout(() => {
        proc.kill("SIGTERM");
        reject(
          new Error(
            `make web-dev did not print dev server URL within 15s. Output:\n${output}`,
          ),
        );
      }, 15_000);

      proc.stdout?.on("data", (data: Buffer) => {
        output += data.toString();
        if (output.includes("localhost") || output.includes("Local:")) {
          clearTimeout(timeout);
          resolve({ process: proc, output });
        }
      });

      proc.stderr?.on("data", (data: Buffer) => {
        output += data.toString();
        // Vite sometimes prints to stderr
        if (output.includes("localhost") || output.includes("Local:")) {
          clearTimeout(timeout);
          resolve({ process: proc, output });
        }
      });

      proc.on("error", (err) => {
        clearTimeout(timeout);
        reject(err);
      });

      proc.on("exit", (code) => {
        clearTimeout(timeout);
        reject(
          new Error(
            `make web-dev exited with code ${code} before printing URL. Output:\n${output}`,
          ),
        );
      });
    });

    devProc = started.process;
    expect(started.output).toMatch(/localhost/i);
  });
});

describe("TS-02-17: make web-build produces production output", () => {
  it("make web-build exits with code 0", () => {
    const result = runCommand("make web-build", REPO_ROOT);
    expect(result.exitCode).toBe(0);
  });

  it("web/dist/ is populated with HTML and JS after make web-build", () => {
    runCommand("make web-build", REPO_ROOT);
    expect(existsSync(join(WEB_DIR, "dist"))).toBe(true);

    const htmlFiles = findFiles(join(WEB_DIR, "dist"), ".html");
    const jsFiles = findFiles(join(WEB_DIR, "dist"), ".js");
    expect(htmlFiles.length).toBeGreaterThanOrEqual(1);
    expect(jsFiles.length).toBeGreaterThanOrEqual(1);
  });
});

describe("TS-02-18: make web-lint exits cleanly", () => {
  it("make web-lint exits with code 0", () => {
    const result = runCommand("make web-lint", REPO_ROOT);
    expect(result.exitCode).toBe(0);
  });
});

/**
 * Recursively find files with a given extension under a directory.
 */
function findFiles(dir: string, ext: string): string[] {
  if (!existsSync(dir)) return [];
  const { readdirSync, statSync } =
    require("node:fs") as typeof import("node:fs");
  const results: string[] = [];
  function walk(d: string): void {
    for (const entry of readdirSync(d)) {
      const full = join(d, entry);
      if (statSync(full).isDirectory()) {
        walk(full);
      } else if (entry.endsWith(ext)) {
        results.push(full);
      }
    }
  }
  walk(dir);
  return results;
}
