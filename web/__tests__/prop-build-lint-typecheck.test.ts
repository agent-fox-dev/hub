/**
 * Property Tests: Build Reproducibility, Lint Baseline, TypeScript Strict Mode
 * (TS-02-P1, TS-02-P2, TS-02-P3)
 *
 * Verifies invariants:
 * - npm run build always exits 0 and produces dist/ with HTML (TS-02-P1)
 * - npm run lint exits 0 with zero errors for all scaffold files (TS-02-P2)
 * - tsc --noEmit exits 0 with strict=true and no TS errors (TS-02-P3)
 *
 * Requirements: 02-REQ-2.6, 02-REQ-5.2, 02-REQ-2.7, 02-REQ-4.1,
 *               02-REQ-5.3, 02-REQ-1.3
 */

import { describe, it, expect } from "vitest";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { execSync } from "node:child_process";
import { resolve, join } from "node:path";

const WEB_DIR = resolve(__dirname, "..");

function runInWebDir(
  cmd: string,
  timeoutMs = 120_000,
): {
  exitCode: number;
  stdout: string;
  stderr: string;
  durationMs: number;
} {
  const start = Date.now();
  try {
    const stdout = execSync(cmd, {
      cwd: WEB_DIR,
      encoding: "utf-8",
      stdio: ["pipe", "pipe", "pipe"],
      timeout: timeoutMs,
    });
    return {
      exitCode: 0,
      stdout,
      stderr: "",
      durationMs: Date.now() - start,
    };
  } catch (err: unknown) {
    const e = err as { status?: number; stdout?: string; stderr?: string };
    return {
      exitCode: e.status ?? 1,
      stdout: e.stdout ?? "",
      stderr: e.stderr ?? "",
      durationMs: Date.now() - start,
    };
  }
}

/**
 * Recursively find files with a given extension under a directory.
 */
function findFiles(dir: string, ext: string): string[] {
  if (!existsSync(dir)) return [];
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

/**
 * Find all TypeScript/TSX source files under web/src/ (excluding test files
 * and config files like vitest.config.ts).
 */
function findSourceFiles(): string[] {
  const srcDir = join(WEB_DIR, "src");
  if (!existsSync(srcDir)) return [];
  const tsFiles = findFiles(srcDir, ".ts");
  const tsxFiles = findFiles(srcDir, ".tsx");
  return [...tsFiles, ...tsxFiles];
}

describe("TS-02-P1: Production build reproducibility", () => {
  it("npm run build exits with code 0", () => {
    const result = runInWebDir("npm run build");
    expect(result.exitCode).toBe(0);
  });

  it("web/dist/ exists after build", () => {
    runInWebDir("npm run build");
    expect(existsSync(join(WEB_DIR, "dist"))).toBe(true);
  });

  it("web/dist/ contains at least one .html file", () => {
    runInWebDir("npm run build");
    const htmlFiles = findFiles(join(WEB_DIR, "dist"), ".html");
    expect(htmlFiles.length).toBeGreaterThanOrEqual(1);
  });

  it("build completes within 120 seconds", () => {
    const result = runInWebDir("npm run build", 120_000);
    expect(result.exitCode).toBe(0);
    expect(result.durationMs).toBeLessThanOrEqual(120_000);
  });
});

describe("TS-02-P2: Lint clean baseline for all scaffold source files", () => {
  it("all .ts/.tsx files in web/src/ pass ESLint with zero errors", () => {
    const sourceFiles = findSourceFiles();
    // There must be at least some source files for this to be meaningful
    expect(sourceFiles.length).toBeGreaterThan(0);

    // Run lint against all source files at once
    const result = runInWebDir("npm run lint", 60_000);
    expect(result.exitCode).toBe(0);

    const output = result.stdout + result.stderr;
    // No errors should be present
    const errorCount = (output.match(/\d+ error/g) || []).filter(
      (m) => !m.startsWith("0 error"),
    );
    expect(errorCount.length).toBe(0);
  });

  it("lint completes within 60 seconds", () => {
    const result = runInWebDir("npm run lint", 60_000);
    expect(result.exitCode).toBe(0);
    expect(result.durationMs).toBeLessThanOrEqual(60_000);
  });
});

describe("TS-02-P3: TypeScript strict mode compliance", () => {
  it("web/tsconfig.json has strict=true", () => {
    const tsconfigPath = join(WEB_DIR, "tsconfig.json");
    expect(existsSync(tsconfigPath)).toBe(true);

    const tsconfig = JSON.parse(
      readFileSync(tsconfigPath, "utf-8"),
    ) as Record<string, unknown>;
    const compilerOptions = tsconfig.compilerOptions as Record<
      string,
      unknown
    >;
    expect(compilerOptions.strict).toBe(true);
  });

  it("npx tsc --noEmit exits with code 0", () => {
    const result = runInWebDir("npx tsc --noEmit");
    expect(result.exitCode).toBe(0);
  });

  it("no TypeScript errors in stdout or stderr", () => {
    const result = runInWebDir("npx tsc --noEmit");
    expect(result.exitCode).toBe(0);
    expect(result.stdout).not.toContain("error TS");
    expect(result.stderr).not.toContain("error TS");
  });
});
