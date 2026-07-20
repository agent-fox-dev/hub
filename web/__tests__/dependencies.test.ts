/**
 * Dependency Installation Tests (TS-02-5 through TS-02-9)
 *
 * Verifies that all required toolchain packages are listed in
 * web/package.json and installed in web/node_modules.
 *
 * Requirements: 02-REQ-2.1, 02-REQ-2.2, 02-REQ-2.3, 02-REQ-2.4, 02-REQ-2.5
 */

import { describe, it, expect } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";

const WEB_DIR = resolve(__dirname, "..");

function readPkgJson(): Record<string, unknown> {
  const content = readFileSync(join(WEB_DIR, "package.json"), "utf-8");
  return JSON.parse(content) as Record<string, unknown>;
}

function getAllDeps(): Record<string, string> {
  const pkg = readPkgJson();
  return {
    ...((pkg.dependencies ?? {}) as Record<string, string>),
    ...((pkg.devDependencies ?? {}) as Record<string, string>),
  };
}

describe("TS-02-5: Vite is listed and installed", () => {
  it("vite is in devDependencies", () => {
    const pkg = readPkgJson();
    const devDeps = (pkg.devDependencies ?? {}) as Record<string, string>;
    expect(devDeps).toHaveProperty("vite");
  });

  it("web/node_modules/vite directory exists", () => {
    expect(existsSync(join(WEB_DIR, "node_modules", "vite"))).toBe(true);
  });
});

describe("TS-02-6: React and React DOM are listed and installed", () => {
  it("react is in dependencies", () => {
    const deps = getAllDeps();
    expect(deps).toHaveProperty("react");
  });

  it("react-dom is in dependencies", () => {
    const deps = getAllDeps();
    expect(deps).toHaveProperty("react-dom");
  });

  it("web/node_modules/react directory exists", () => {
    expect(existsSync(join(WEB_DIR, "node_modules", "react"))).toBe(true);
  });

  it("web/node_modules/react-dom directory exists", () => {
    expect(existsSync(join(WEB_DIR, "node_modules", "react-dom"))).toBe(true);
  });
});

describe("TS-02-7: Tailwind CSS is listed and installed", () => {
  it("tailwindcss is in dependencies or devDependencies", () => {
    const deps = getAllDeps();
    expect(deps).toHaveProperty("tailwindcss");
  });

  it("web/node_modules/tailwindcss directory exists", () => {
    expect(
      existsSync(join(WEB_DIR, "node_modules", "tailwindcss")),
    ).toBe(true);
  });
});

describe("TS-02-8: TanStack Query is listed and installed", () => {
  it("@tanstack/react-query is in dependencies", () => {
    const deps = getAllDeps();
    expect(deps).toHaveProperty("@tanstack/react-query");
  });

  it("web/node_modules/@tanstack/react-query directory exists", () => {
    expect(
      existsSync(join(WEB_DIR, "node_modules", "@tanstack", "react-query")),
    ).toBe(true);
  });
});

describe("TS-02-9: React Router is listed and installed", () => {
  it("react-router-dom or react-router is in dependencies", () => {
    const deps = getAllDeps();
    const hasReactRouter =
      "react-router-dom" in deps || "react-router" in deps;
    expect(hasReactRouter).toBe(true);
  });

  it("web/node_modules/react-router-dom or react-router directory exists", () => {
    const hasDir =
      existsSync(join(WEB_DIR, "node_modules", "react-router-dom")) ||
      existsSync(join(WEB_DIR, "node_modules", "react-router"));
    expect(hasDir).toBe(true);
  });
});
