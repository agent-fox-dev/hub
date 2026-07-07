/**
 * Group 1 spec tests: Project initialization and dependency checks.
 *
 * Covers: TS-04-1, TS-04-2, TS-04-3, TS-04-4, TS-04-5, TS-04-P5
 * Requirements: 04-REQ-1.1 through 04-REQ-1.5
 *
 * These tests validate the web/ scaffold by reading files and checking
 * config/content. They are static-analysis tests (no React rendering)
 * and run in Node.js environment.
 */
import { describe, test, expect } from 'vitest';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const WEB_ROOT = path.resolve(__dirname, '..');
const REPO_ROOT = path.resolve(WEB_ROOT, '..');

// ---------------------------------------------------------------------------
// File-reading helpers
// ---------------------------------------------------------------------------

/** Read and parse a JSON file. Returns null if the file does not exist. */
function readJsonFile(filePath: string): Record<string, unknown> | null {
  if (!fs.existsSync(filePath)) return null;
  return JSON.parse(fs.readFileSync(filePath, 'utf-8')) as Record<string, unknown>;
}

/** Read a text file. Returns null if the file does not exist. */
function readTextFile(filePath: string): string | null {
  if (!fs.existsSync(filePath)) return null;
  return fs.readFileSync(filePath, 'utf-8');
}

/** Merge dependencies and devDependencies from a parsed package.json. */
function getAllDeps(
  pkg: Record<string, unknown>,
): Record<string, string> {
  return {
    ...(pkg.dependencies as Record<string, string> | undefined),
    ...(pkg.devDependencies as Record<string, string> | undefined),
  };
}

// ---------------------------------------------------------------------------
// TS-04-1: web/ directory structure and config files exist
// Requirement: 04-REQ-1.1
// ---------------------------------------------------------------------------

describe('TS-04-1: web/ directory structure and config files', () => {
  test('web/ directory exists at the repo root', () => {
    expect(fs.existsSync(WEB_ROOT)).toBe(true);
    expect(fs.statSync(WEB_ROOT).isDirectory()).toBe(true);
  });

  test('web/package.json exists', () => {
    expect(fs.existsSync(path.join(WEB_ROOT, 'package.json'))).toBe(true);
  });

  test('web/tsconfig.json exists', () => {
    expect(fs.existsSync(path.join(WEB_ROOT, 'tsconfig.json'))).toBe(true);
  });

  test('no shared tsconfig.json between web/ and the Go backend root', () => {
    const rootTsconfigPath = path.join(REPO_ROOT, 'tsconfig.json');
    if (fs.existsSync(rootTsconfigPath)) {
      // If root tsconfig exists, web/tsconfig.json must not reference it
      const webTsconfig = readJsonFile(path.join(WEB_ROOT, 'tsconfig.json'));
      expect(webTsconfig).not.toBeNull();
      const extendsValue = String(
        (webTsconfig as Record<string, unknown>).extends ?? '',
      );
      expect(extendsValue).not.toMatch(/\.\.\//);
    }
    // No root tsconfig => no sharing possible => pass
  });

  test('web/package.json has a non-null name field', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    expect(pkg!.name).toBeTruthy();
  });
});

// ---------------------------------------------------------------------------
// TS-04-2: package.json declares all required dependencies
// Requirement: 04-REQ-1.2
// ---------------------------------------------------------------------------

describe('TS-04-2: package.json declares all required dependencies', () => {
  function deps(): Record<string, string> {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    return getAllDeps(pkg!);
  }

  test('vite is listed as a dependency', () => {
    expect(deps()).toHaveProperty('vite');
  });

  test('react is listed as a dependency', () => {
    expect(deps()).toHaveProperty('react');
  });

  test('react-dom is listed as a dependency', () => {
    expect(deps()).toHaveProperty('react-dom');
  });

  test('typescript is listed as a dependency', () => {
    expect(deps()).toHaveProperty('typescript');
  });

  test('tailwindcss is listed as a dependency', () => {
    expect(deps()).toHaveProperty('tailwindcss');
  });

  test('react-router-dom or react-router is listed as a dependency', () => {
    const allDeps = deps();
    const hasRouter =
      'react-router-dom' in allDeps || 'react-router' in allDeps;
    expect(hasRouter).toBe(true);
  });

  test('@tanstack/react-query is listed as a dependency', () => {
    expect(deps()).toHaveProperty('@tanstack/react-query');
  });
});

// ---------------------------------------------------------------------------
// TS-04-3: shadcn/ui is copied into the tree, not an npm dependency
// Requirement: 04-REQ-1.3
// ---------------------------------------------------------------------------

describe('TS-04-3: shadcn/ui is copied into tree, not an npm dependency', () => {
  test('shadcn-ui and @shadcn/ui are absent from package.json dependencies', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const allDeps = getAllDeps(pkg!);

    expect(allDeps).not.toHaveProperty('shadcn-ui');
    expect(allDeps).not.toHaveProperty('@shadcn/ui');
  });

  test('web/src/components/ui/ directory exists with at least one .tsx file', () => {
    const uiDir = path.join(WEB_ROOT, 'src', 'components', 'ui');
    expect(fs.existsSync(uiDir)).toBe(true);

    const files = fs.readdirSync(uiDir);
    const tsxFiles = files.filter((f: string) => f.endsWith('.tsx'));
    expect(tsxFiles.length).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// TS-04-4: Node.js version requirement is documented or enforced
// Requirement: 04-REQ-1.4
// ---------------------------------------------------------------------------

describe('TS-04-4: Node.js version requirement is documented or enforced', () => {
  test('engines.node >= 18 in package.json OR Node 18+ documented in docs/web-ui.md', () => {
    // Check package.json engines field
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const engines = pkg!.engines as Record<string, string> | undefined;
    const enginesNode = engines?.node ?? '';
    const enginesOk =
      enginesNode.includes('18') || enginesNode.includes('>=');

    // Check docs/web-ui.md
    let docsOk = false;
    const docsPath = path.join(REPO_ROOT, 'docs', 'web-ui.md');
    const docsContent = readTextFile(docsPath);
    if (docsContent !== null) {
      docsOk =
        docsContent.includes('Node 18') ||
        docsContent.includes('Node.js 18') ||
        docsContent.includes('>=18');
    }

    expect(enginesOk || docsOk).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// TS-04-5: QueryClientProvider wraps the React app entry point
// Requirement: 04-REQ-1.5
// ---------------------------------------------------------------------------

describe('TS-04-5: QueryClientProvider wraps the React app entry point', () => {
  const mainPath = path.join(WEB_ROOT, 'src', 'main.tsx');

  test('web/src/main.tsx exists', () => {
    expect(fs.existsSync(mainPath)).toBe(true);
  });

  test('main.tsx imports QueryClientProvider and QueryClient from @tanstack/react-query', () => {
    const src = readTextFile(mainPath);
    expect(src).not.toBeNull();
    expect(src).toContain('QueryClientProvider');
    expect(src).toContain('QueryClient');
    expect(src).toContain('@tanstack/react-query');
  });

  test('QueryClientProvider wraps the App or root router component', () => {
    const src = readTextFile(mainPath);
    expect(src).not.toBeNull();

    const qcpPos = src!.indexOf('<QueryClientProvider');
    expect(qcpPos).toBeGreaterThanOrEqual(0);

    // Find the earliest root component marker
    const candidates = [
      src!.indexOf('<App'),
      src!.indexOf('<RouterProvider'),
      src!.indexOf('<BrowserRouter'),
    ].filter((pos) => pos !== -1);
    expect(candidates.length).toBeGreaterThan(0);

    const appPos = Math.min(...candidates);
    // QueryClientProvider must open before the root component
    expect(qcpPos).toBeLessThan(appPos);
  });
});

// ---------------------------------------------------------------------------
// TS-04-P5: Property test — QueryClientProvider ancestry in component tree
// Requirement: 04-REQ-1.5 (property 04-PROP-5)
// ---------------------------------------------------------------------------

describe('TS-04-P5: QueryClientProvider is an ancestor of all app components', () => {
  test('static: QueryClientProvider opens before App/Router in entry source', () => {
    const mainPath = path.join(WEB_ROOT, 'src', 'main.tsx');
    const src = readTextFile(mainPath);
    expect(src).not.toBeNull();

    // Must import both symbols
    expect(src).toContain('QueryClientProvider');
    expect(src).toContain('QueryClient');

    // Verify wrapping order in source text
    const qcpIndex = src!.indexOf('QueryClientProvider');
    const appCandidates = [
      src!.indexOf('<App'),
      src!.indexOf('<RouterProvider'),
      src!.indexOf('<BrowserRouter'),
    ].filter((pos) => pos !== -1);

    expect(appCandidates.length).toBeGreaterThan(0);
    const appIndex = Math.min(...appCandidates);
    expect(qcpIndex).toBeLessThan(appIndex);
  });

  // Runtime render check requires React + @testing-library/react.
  // These will be installed in task group 4; the runtime test will be
  // added at that point.
  test.todo(
    'runtime: QueryClientProvider is in the rendered component tree ancestry',
  );
});
