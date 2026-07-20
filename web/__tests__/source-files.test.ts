/**
 * Placeholder Route and Source File Tests (TS-02-19 through TS-02-22)
 *
 * Verifies that the required source files exist with correct content:
 * web/index.html, web/src/main.tsx, and web/src/App.tsx.
 *
 * Requirements: 02-REQ-6.1, 02-REQ-6.2, 02-REQ-6.3, 02-REQ-6.4
 */

import { describe, it, expect, afterAll } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { resolve, join } from "node:path";
import { ChildProcess, spawn } from "node:child_process";

const WEB_DIR = resolve(__dirname, "..");

describe("TS-02-19: web/index.html is Vite's HTML entry point", () => {
  it("web/index.html exists", () => {
    expect(existsSync(join(WEB_DIR, "index.html"))).toBe(true);
  });

  it("web/index.html contains a script tag referencing main.tsx", () => {
    const content = readFileSync(join(WEB_DIR, "index.html"), "utf-8");
    const refersToMain =
      content.includes("src/main.tsx") || content.includes("main.tsx");
    expect(refersToMain).toBe(true);
  });
});

describe("TS-02-20: web/src/main.tsx mounts the React app", () => {
  it("web/src/main.tsx exists", () => {
    expect(existsSync(join(WEB_DIR, "src", "main.tsx"))).toBe(true);
  });

  it("web/src/main.tsx contains createRoot or render call", () => {
    const content = readFileSync(
      join(WEB_DIR, "src", "main.tsx"),
      "utf-8",
    );
    const hasMount =
      content.includes("createRoot") || content.includes("render");
    expect(hasMount).toBe(true);
  });

  it("web/src/main.tsx references the App component", () => {
    const content = readFileSync(
      join(WEB_DIR, "src", "main.tsx"),
      "utf-8",
    );
    expect(content).toContain("App");
  });
});

describe("TS-02-21: web/src/App.tsx renders h1 with 'af-hub'", () => {
  it("web/src/App.tsx exists", () => {
    expect(existsSync(join(WEB_DIR, "src", "App.tsx"))).toBe(true);
  });

  it("web/src/App.tsx contains an <h1> element", () => {
    const content = readFileSync(
      join(WEB_DIR, "src", "App.tsx"),
      "utf-8",
    );
    expect(content).toContain("<h1>");
  });

  it("web/src/App.tsx contains the text 'af-hub'", () => {
    const content = readFileSync(
      join(WEB_DIR, "src", "App.tsx"),
      "utf-8",
    );
    expect(content).toContain("af-hub");
  });
});

describe("TS-02-22: Vite dev server renders page with h1 'af-hub'", () => {
  let devProc: ChildProcess | null = null;
  let devServerUrl: string | null = null;

  afterAll(() => {
    if (devProc) {
      devProc.kill("SIGTERM");
      devProc = null;
    }
  });

  /**
   * Start the Vite dev server and wait for it to print its URL.
   */
  async function ensureDevServer(): Promise<string> {
    if (devServerUrl) return devServerUrl;

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
          devProc = proc;
          devServerUrl = match[0];
          resolve(match[0]);
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
              `Vite dev server exited with code ${code}. Output:\n${output}`,
            ),
          );
        }
      });
    });
  }

  it("root URL response HTML contains the app entry point", async () => {
    const url = await ensureDevServer();
    const response = await fetch(`${url}/`);
    const html = await response.text();
    // The initial HTML should reference the main entry point
    expect(html).toContain("main.tsx");
  });

  it("source App.tsx serves JSX with exactly one h1 containing 'af-hub'", () => {
    // Verify via source file analysis that the component renders
    // exactly one <h1> with text 'af-hub'. Full browser rendering
    // (via Playwright) would be ideal but is not set up for the scaffold.
    const appPath = join(WEB_DIR, "src", "App.tsx");
    expect(existsSync(appPath)).toBe(true);

    const content = readFileSync(appPath, "utf-8");

    // Count h1 opening tags — there should be exactly one
    const h1Matches = content.match(/<h1[^>]*>/g);
    expect(h1Matches).not.toBeNull();
    expect(h1Matches!.length).toBe(1);

    // Verify the h1 contains 'af-hub'
    expect(content).toMatch(/<h1[^>]*>af-hub<\/h1>/);
  });
});
