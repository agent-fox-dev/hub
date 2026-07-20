/**
 * Build and Lint Execution Tests (TS-02-10, TS-02-11)
 *
 * Verifies that npm run build and npm run lint execute successfully,
 * producing the expected output artifacts and exit codes.
 *
 * Requirements: 02-REQ-2.6, 02-REQ-2.7
 */

import { describe, it, expect } from "vitest";
import { execSync } from "node:child_process";
import { existsSync } from "node:fs";
import { join, resolve } from "node:path";

const WEB_DIR = resolve(__dirname, "..");

function runInWebDir(cmd: string): { exitCode: number; output: string } {
  try {
    const output = execSync(cmd, {
      cwd: WEB_DIR,
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

describe("TS-02-10: npm run build produces production output", () => {
  it("npm run build exits with code 0", () => {
    const result = runInWebDir("npm run build");
    expect(result.exitCode).toBe(0);
  });

  it("web/dist/ directory exists after build", () => {
    // Run build first to ensure dist is created
    runInWebDir("npm run build");
    expect(existsSync(join(WEB_DIR, "dist"))).toBe(true);
  });

  it("web/dist/ contains at least one .html file", () => {
    runInWebDir("npm run build");
    const htmlFiles = findFiles(join(WEB_DIR, "dist"), ".html");
    expect(htmlFiles.length).toBeGreaterThanOrEqual(1);
  });

  it("web/dist/ contains at least one .js file", () => {
    runInWebDir("npm run build");
    const jsFiles = findFiles(join(WEB_DIR, "dist"), ".js");
    expect(jsFiles.length).toBeGreaterThanOrEqual(1);
  });
});

describe("TS-02-11: npm run lint exits cleanly", () => {
  it("npm run lint exits with code 0 and reports no errors", () => {
    const result = runInWebDir("npm run lint");
    expect(result.exitCode).toBe(0);
  });
});

/**
 * Recursively find files with a given extension under a directory.
 */
function findFiles(dir: string, ext: string): string[] {
  if (!existsSync(dir)) return [];
  const { readdirSync, statSync } = require("node:fs") as typeof import("node:fs");
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
