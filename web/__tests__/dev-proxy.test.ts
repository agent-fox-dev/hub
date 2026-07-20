/**
 * Dev Proxy Configuration Tests (TS-02-12, TS-02-13)
 *
 * Verifies that the Vite dev server proxy is configured to forward
 * /api, /healthz, and /readyz requests to the Go backend at
 * http://localhost:8080.
 *
 * Requirements: 02-REQ-3.1, 02-REQ-3.2
 */

import { describe, it, expect, afterAll } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { resolve, join } from "node:path";
import { createServer as createHttpServer } from "node:http";
import type { Server } from "node:http";
import { ChildProcess, spawn } from "node:child_process";

const WEB_DIR = resolve(__dirname, "..");

/**
 * Find the Vite config file in web/ (could be vite.config.ts, vite.config.js,
 * vite.config.mjs, etc.)
 */
function findViteConfig(): string | null {
  const candidates = [
    "vite.config.ts",
    "vite.config.js",
    "vite.config.mjs",
    "vite.config.mts",
  ];
  for (const name of candidates) {
    const fullPath = join(WEB_DIR, name);
    if (existsSync(fullPath)) return fullPath;
  }
  return null;
}

describe("TS-02-13: Vite config contains proxy configuration", () => {
  it("a Vite config file exists in web/", () => {
    const configPath = findViteConfig();
    expect(configPath).not.toBeNull();
  });

  it("Vite config contains '/api' proxy path", () => {
    const configPath = findViteConfig();
    expect(configPath).not.toBeNull();
    const content = readFileSync(configPath!, "utf-8");
    expect(content).toContain("/api");
  });

  it("Vite config contains '/healthz' proxy path", () => {
    const configPath = findViteConfig();
    expect(configPath).not.toBeNull();
    const content = readFileSync(configPath!, "utf-8");
    expect(content).toContain("/healthz");
  });

  it("Vite config contains '/readyz' proxy path", () => {
    const configPath = findViteConfig();
    expect(configPath).not.toBeNull();
    const content = readFileSync(configPath!, "utf-8");
    expect(content).toContain("/readyz");
  });

  it("Vite config contains backend target 'http://localhost:8080'", () => {
    const configPath = findViteConfig();
    expect(configPath).not.toBeNull();
    const content = readFileSync(configPath!, "utf-8");
    expect(content).toContain("http://localhost:8080");
  });
});

describe("TS-02-12: Dev proxy forwards requests to Go backend", () => {
  let mockBackend: Server | null = null;
  let devServer: ChildProcess | null = null;
  let devServerUrl: string | null = null;

  afterAll(async () => {
    if (devServer) {
      devServer.kill("SIGTERM");
      devServer = null;
    }
    if (mockBackend) {
      await new Promise<void>((resolve) => mockBackend!.close(() => resolve()));
      mockBackend = null;
    }
  });

  /**
   * Start a mock HTTP server on port 8080 that returns a sentinel body
   * for /api, /healthz, and /readyz.
   */
  async function startMockBackend(): Promise<Server> {
    return new Promise((resolve, reject) => {
      const server = createHttpServer((req, res) => {
        const sentinel = `mock-backend-response:${req.url}`;
        res.writeHead(200, { "Content-Type": "text/plain" });
        res.end(sentinel);
      });
      server.on("error", reject);
      server.listen(8080, "127.0.0.1", () => resolve(server));
    });
  }

  /**
   * Start the Vite dev server and wait for it to print its URL.
   */
  async function startDevServer(): Promise<{ process: ChildProcess; url: string }> {
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
        // Vite prints something like "Local: http://localhost:5173/"
        const match = output.match(/https?:\/\/localhost:\d+/);
        if (match) {
          clearTimeout(timeout);
          resolve({ process: proc, url: match[0] });
        }
      });

      proc.stderr?.on("data", (data: Buffer) => {
        output += data.toString();
      });

      proc.on("error", (err) => {
        clearTimeout(timeout);
        reject(err);
      });

      proc.on("exit", (code) => {
        if (!devServerUrl) {
          clearTimeout(timeout);
          reject(
            new Error(
              `Vite dev server exited with code ${code} before printing URL. Output:\n${output}`,
            ),
          );
        }
      });
    });
  }

  const PROXY_PATHS = ["/healthz", "/readyz", "/api"];

  for (const path of PROXY_PATHS) {
    it(`forwards ${path} to the Go backend and returns its response`, async () => {
      // Start mock backend if not already running
      if (!mockBackend) {
        mockBackend = await startMockBackend();
      }

      // Start dev server if not already running
      if (!devServer) {
        const started = await startDevServer();
        devServer = started.process;
        devServerUrl = started.url;
      }

      // Make request through the Vite dev server
      const response = await fetch(`${devServerUrl}${path}`);
      const body = await response.text();

      expect(response.status).toBe(200);
      expect(body).toBe(`mock-backend-response:${path}`);
    });
  }
});
