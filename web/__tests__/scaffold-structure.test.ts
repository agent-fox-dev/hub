/**
 * Scaffold Structure Tests (TS-02-1 through TS-02-4)
 *
 * Verifies that the web/ directory is properly initialized as a standalone
 * frontend project with the correct package.json, tsconfig.json, and
 * shadcn/ui configuration.
 *
 * Requirements: 02-REQ-1.1, 02-REQ-1.2, 02-REQ-1.3, 02-REQ-1.4
 */

import { describe, it, expect } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";

const WEB_DIR = resolve(__dirname, "..");
const REPO_ROOT = resolve(WEB_DIR, "..");

function readJson(filePath: string): Record<string, unknown> {
  const content = readFileSync(filePath, "utf-8");
  return JSON.parse(content) as Record<string, unknown>;
}

describe("TS-02-1: web/ directory contains standalone frontend project", () => {
  it("web/package.json exists", () => {
    expect(existsSync(join(WEB_DIR, "package.json"))).toBe(true);
  });

  it("web/package.json is valid JSON with a 'name' field", () => {
    const pkg = readJson(join(WEB_DIR, "package.json"));
    expect(pkg).toBeDefined();
    expect(pkg).toHaveProperty("name");
    expect(typeof pkg.name).toBe("string");
  });

  it("no frontend package.json exists at the repository root", () => {
    const rootPkgPath = join(REPO_ROOT, "package.json");
    if (existsSync(rootPkgPath)) {
      const rootPkg = readJson(rootPkgPath);
      const devDeps = (rootPkg.devDependencies ?? {}) as Record<string, string>;
      // If a root package.json exists, it must not contain frontend tooling
      expect(devDeps).not.toHaveProperty("vite");
    }
    // If no root package.json exists, that's also valid
  });
});

describe("TS-02-2: npm is the package manager with committed lockfile", () => {
  it("web/package-lock.json exists and is valid JSON", () => {
    const lockPath = join(WEB_DIR, "package-lock.json");
    expect(existsSync(lockPath)).toBe(true);
    const lock = readJson(lockPath);
    expect(lock).toBeDefined();
  });

  it("no yarn.lock exists in web/", () => {
    expect(existsSync(join(WEB_DIR, "yarn.lock"))).toBe(false);
  });

  it("no pnpm-lock.yaml exists in web/", () => {
    expect(existsSync(join(WEB_DIR, "pnpm-lock.yaml"))).toBe(false);
  });
});

describe("TS-02-3: tsconfig.json has required compiler options", () => {
  it("web/tsconfig.json exists", () => {
    expect(existsSync(join(WEB_DIR, "tsconfig.json"))).toBe(true);
  });

  it("compilerOptions.strict is true", () => {
    const tsconfig = readJson(join(WEB_DIR, "tsconfig.json"));
    const options = tsconfig.compilerOptions as Record<string, unknown>;
    expect(options.strict).toBe(true);
  });

  it("compilerOptions.moduleResolution is 'bundler'", () => {
    const tsconfig = readJson(join(WEB_DIR, "tsconfig.json"));
    const options = tsconfig.compilerOptions as Record<string, unknown>;
    expect((options.moduleResolution as string).toLowerCase()).toBe("bundler");
  });

  it("compilerOptions.target is 'ESNext'", () => {
    const tsconfig = readJson(join(WEB_DIR, "tsconfig.json"));
    const options = tsconfig.compilerOptions as Record<string, unknown>;
    expect(options.target).toBe("ESNext");
  });

  it("compilerOptions.jsx is 'react-jsx'", () => {
    const tsconfig = readJson(join(WEB_DIR, "tsconfig.json"));
    const options = tsconfig.compilerOptions as Record<string, unknown>;
    expect(options.jsx).toBe("react-jsx");
  });
});

describe("TS-02-4: web/components.json confirms shadcn/ui init", () => {
  it("web/components.json exists", () => {
    expect(existsSync(join(WEB_DIR, "components.json"))).toBe(true);
  });

  it("web/components.json is valid JSON with shadcn/ui config keys", () => {
    const config = readJson(join(WEB_DIR, "components.json"));
    expect(config).toBeDefined();
    // shadcn/ui config should have 'style' or '$schema'
    const hasExpectedKeys =
      "$schema" in config || "style" in config;
    expect(hasExpectedKeys).toBe(true);
  });
});
