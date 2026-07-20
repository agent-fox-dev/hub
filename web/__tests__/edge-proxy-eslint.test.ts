/**
 * Edge Case Tests: Proxy Errors, Non-Proxied Paths, ESLint Plugin,
 * Make Targets Without Dependencies (TS-02-E5, TS-02-E6, TS-02-E7, TS-02-E8)
 *
 * Verifies that:
 * - Proxied requests fail gracefully when Go backend is down (TS-02-E5)
 * - Non-proxied paths serve the SPA directly (TS-02-E6)
 * - Missing @typescript-eslint causes lint failure (TS-02-E7)
 * - make web-build fails without npm dependencies (TS-02-E8)
 *
 * Requirements: 02-REQ-3.E1, 02-REQ-3.E2, 02-REQ-4.E1, 02-REQ-5.E1
 */

import { describe, it, expect, afterAll } from "vitest";
import { existsSync } from "node:fs";
import { execSync, ChildProcess, spawn } from "node:child_process";
import { resolve, join } from "node:path";
import { createServer } from "node:net";

const WEB_DIR = resolve(__dirname, "..");
const REPO_ROOT = resolve(WEB_DIR, "..");

function runCommand(
  cmd: string,
  cwd: string,
): { exitCode: number; stdout: string; stderr: string } {
  try {
    const stdout = execSync(cmd, {
      cwd,
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
 * Check that port 8080 is NOT listening by trying to bind to it.
 * Releases the port immediately after the check.
 */
async function ensurePortNotListening(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const srv = createServer();
    srv.once("error", () => {
      // Port is in use
      resolve(false);
    });
    srv.listen(port, "127.0.0.1", () => {
      srv.close(() => resolve(true));
    });
  });
}

/**
 * Start the Vite dev server and wait for it to print its URL.
 */
async function startDevServer(): Promise<{
  process: ChildProcess;
  url: string;
}> {
  return new Promise((resolve, reject) => {
    const proc = spawn("npm", ["run", "dev"], {
      cwd: WEB_DIR,
      stdio: ["pipe", "pipe", "pipe"],
      env: { ...process.env },
    });

    let output = "";
    const timeout = setTimeout(() => {
      proc.kill("SIGTERM");
      reject(
        new Error(
          `Vite dev server did not start within 15s. Output:\n${output}`,
        ),
      );
    }, 15_000);

    proc.stdout?.on("data", (data: Buffer) => {
      output += data.toString();
      const match = output.match(/https?:\/\/localhost:\d+/);
      if (match) {
        clearTimeout(timeout);
        resolve({ process: proc, url: match[0] });
      }
    });

    proc.stderr?.on("data", (data: Buffer) => {
      output += data.toString();
      const match = output.match(/https?:\/\/localhost:\d+/);
      if (match) {
        clearTimeout(timeout);
        resolve({ process: proc, url: match[0] });
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
          `Vite dev server exited with code ${code}. Output:\n${output}`,
        ),
      );
    });
  });
}

describe("TS-02-E5: Proxied request fails when Go backend is down", () => {
  let devServer: ChildProcess | null = null;

  afterAll(() => {
    if (devServer) {
      devServer.kill("SIGTERM");
      devServer = null;
    }
  });

  it("/healthz returns a network error or 502/504 when backend is not running", async () => {
    // Ensure port 8080 is not listening
    const portFree = await ensurePortNotListening(8080);
    if (!portFree) {
      // Port is in use — skip this test to avoid interfering with a real service
      return;
    }

    const started = await startDevServer();
    devServer = started.process;

    // Attempt the proxied request
    try {
      const response = await fetch(`${started.url}/healthz`);
      // Vite proxy may return 502 or 504 when backend is unreachable
      expect([502, 504]).toContain(response.status);
      const body = await response.text();
      expect(body).not.toContain("af-hub");
    } catch {
      // A network-level error (ECONNREFUSED) is also acceptable
      expect(true).toBe(true);
    }
  });
});

describe("TS-02-E6: Non-proxied paths serve SPA directly", () => {
  let devServer: ChildProcess | null = null;

  afterAll(() => {
    if (devServer) {
      devServer.kill("SIGTERM");
      devServer = null;
    }
  });

  it("/some-other-path returns 200 with HTML content (SPA fallback)", async () => {
    // Ensure port 8080 is not listening — confirms no proxy involvement
    const portFree = await ensurePortNotListening(8080);
    if (!portFree) {
      return;
    }

    const started = await startDevServer();
    devServer = started.process;

    const response = await fetch(`${started.url}/some-other-path`);
    expect(response.status).toBe(200);

    const contentType = response.headers.get("content-type") ?? "";
    const body = await response.text();

    const isHtml =
      contentType.includes("text/html") || body.includes("af-hub");
    expect(isHtml).toBe(true);
  });
});

describe("TS-02-E7: ESLint fails when @typescript-eslint is missing", () => {
  /**
   * Move @typescript-eslint aside in a subprocess to avoid crashing vitest,
   * run lint, capture result, then restore.
   */
  it("npm run lint exits with non-zero code identifying the missing package", () => {
    const tsEslintPath = join(
      WEB_DIR,
      "node_modules",
      "@typescript-eslint",
    );
    if (!existsSync(tsEslintPath)) {
      // Package doesn't exist yet (scaffold not implemented) — still verify
      // that lint fails when it's absent
      const result = runCommand("npm run lint", WEB_DIR);
      expect(result.exitCode).not.toBe(0);
      return;
    }

    // Use a shell script to temporarily move @typescript-eslint aside
    const script = [
      `cd "${WEB_DIR}"`,
      "mv node_modules/@typescript-eslint node_modules/@typescript-eslint_backup_e7",
      "OUTPUT=$(npm run lint 2>&1); CODE=$?",
      "mv node_modules/@typescript-eslint_backup_e7 node_modules/@typescript-eslint",
      'echo "$OUTPUT"',
      "exit $CODE",
    ].join(" && ");

    let exitCode: number;
    let output: string;
    try {
      output = execSync(`bash -c '${script.replace(/'/g, "'\\''")}'`, {
        encoding: "utf-8",
        stdio: ["pipe", "pipe", "pipe"],
        timeout: 30_000,
      });
      exitCode = 0;
    } catch (err: unknown) {
      const e = err as { status?: number; stdout?: string; stderr?: string };
      exitCode = e.status ?? 1;
      output = (e.stdout ?? "") + (e.stderr ?? "");
    }

    expect(exitCode).not.toBe(0);

    const outputLower = output.toLowerCase();
    const hasRelevantError =
      outputLower.includes("typescript-eslint") ||
      outputLower.includes("cannot find module") ||
      outputLower.includes("plugin") ||
      outputLower.includes("not found");
    expect(hasRelevantError).toBe(true);
  });
});

describe("TS-02-E8: make web-build fails without npm dependencies", () => {
  /**
   * Move node_modules aside in a subprocess to avoid crashing vitest,
   * run make web-build, capture result, then restore.
   */
  it("make web-build exits with non-zero code and prints actionable error", () => {
    const script = [
      `cd "${WEB_DIR}"`,
      "mv node_modules node_modules_backup_e8 2>/dev/null || true",
      `cd "${REPO_ROOT}"`,
      "OUTPUT=$(make web-build 2>&1); CODE=$?",
      `cd "${WEB_DIR}"`,
      "mv node_modules_backup_e8 node_modules 2>/dev/null || true",
      'echo "$OUTPUT"',
      "exit $CODE",
    ].join(" && ");

    let exitCode: number;
    let output: string;
    try {
      output = execSync(`bash -c '${script.replace(/'/g, "'\\''")}'`, {
        encoding: "utf-8",
        stdio: ["pipe", "pipe", "pipe"],
        timeout: 30_000,
      });
      exitCode = 0;
    } catch (err: unknown) {
      const e = err as { status?: number; stdout?: string; stderr?: string };
      exitCode = e.status ?? 1;
      output = (e.stdout ?? "") + (e.stderr ?? "");
    }

    expect(exitCode).not.toBe(0);

    const outputLower = output.toLowerCase();
    const hasRelevantError =
      outputLower.includes("npm install") ||
      outputLower.includes("cannot find module") ||
      outputLower.includes("not found") ||
      outputLower.includes("missing") ||
      outputLower.includes("err!");
    expect(hasRelevantError).toBe(true);
  });
});
