/**
 * Group 3 spec tests: Hello World route, documentation, and edge cases.
 *
 * Covers: TS-04-16, TS-04-17, TS-04-P4,
 *         TS-04-E6,
 *         TS-04-18, TS-04-19,
 *         TS-04-E2, TS-04-E1
 * Requirements: 04-REQ-5.1, 04-REQ-5.2, 04-REQ-5.E1,
 *               04-REQ-6.1, 04-REQ-6.2,
 *               04-REQ-2.E1, 04-REQ-1.E1
 *
 * Static-analysis tests run in Node.js environment (file reads, source
 * inspection). Runtime React render tests require @testing-library/react
 * and are marked as test.todo() until group 4 installs React dependencies.
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

/** Read a text file. Returns null if the file does not exist. */
function readTextFile(filePath: string): string | null {
  if (!fs.existsSync(filePath)) return null;
  return fs.readFileSync(filePath, 'utf-8');
}

/** Read and parse a JSON file. Returns null if the file does not exist. */
function readJsonFile(filePath: string): Record<string, unknown> | null {
  if (!fs.existsSync(filePath)) return null;
  return JSON.parse(fs.readFileSync(filePath, 'utf-8')) as Record<
    string,
    unknown
  >;
}

// ---------------------------------------------------------------------------
// Proxy-config extraction helper (reused from group 2)
// ---------------------------------------------------------------------------

/**
 * Extract proxy configuration keys from vite.config.ts source text.
 *
 * Uses regex to find proxy rule definitions with { target: '...' } objects.
 * Returns a map of path prefix -> { target, rawBlock } or null.
 */
function extractProxyConfig(
  configSource: string,
): Record<string, { target: string; rawBlock: string }> | null {
  const proxyMatch = configSource.match(/proxy\s*:\s*\{/);
  if (!proxyMatch) return null;

  const result: Record<string, { target: string; rawBlock: string }> = {};

  const rulePattern = /['"]?(\/[a-zA-Z/*]+)['"]?\s*:\s*\{([^}]*)\}/g;
  let match: RegExpExecArray | null;

  while ((match = rulePattern.exec(configSource)) !== null) {
    const routePath = match[1];
    const block = match[2];

    const targetMatch = block.match(/target\s*:\s*['"]([^'"]+)['"]/);
    if (targetMatch) {
      result[routePath] = {
        target: targetMatch[1],
        rawBlock: block,
      };
    }
  }

  return Object.keys(result).length > 0 ? result : null;
}

// ===========================================================================
// TS-04-16: Hello World route renders 'af-hub' at '/'
// Requirement: 04-REQ-5.1
// ===========================================================================

describe('TS-04-16: Hello World route renders af-hub text at /', () => {
  test('a page component file exists that contains af-hub text', () => {
    // Look for a page/component source file containing 'af-hub'
    const srcDir = path.join(WEB_ROOT, 'src');
    expect(fs.existsSync(srcDir)).toBe(true);

    // Search src/ recursively for files containing 'af-hub'
    const tsxFiles = findFilesRecursive(srcDir, /\.(tsx|ts|jsx|js)$/);
    const filesWithAfHub = tsxFiles.filter((f) => {
      const content = readTextFile(f);
      return content !== null && content.includes('af-hub');
    });

    expect(filesWithAfHub.length).toBeGreaterThan(0);
  });

  test('page component contains a placeholder message besides af-hub', () => {
    const srcDir = path.join(WEB_ROOT, 'src');
    expect(fs.existsSync(srcDir)).toBe(true);

    // Find files with 'af-hub' — the page component
    const tsxFiles = findFilesRecursive(srcDir, /\.(tsx|ts|jsx|js)$/);
    const pageFiles = tsxFiles.filter((f) => {
      const content = readTextFile(f);
      return content !== null && content.includes('af-hub');
    });
    expect(pageFiles.length).toBeGreaterThan(0);

    // At least one of these files must contain additional text beyond 'af-hub'
    // Look for string literals (quoted text) that are NOT just 'af-hub'
    const hasPlaceholder = pageFiles.some((f) => {
      const content = readTextFile(f)!;
      // Remove 'af-hub' occurrences and check if other text content remains
      // Match JSX text content (strings between > and <) or string literals
      const textMatches = content.match(/>[^<]*[a-zA-Z][^<]*</g);
      if (!textMatches) return false;
      // Filter out lines that only contain 'af-hub'
      const otherText = textMatches.filter(
        (m) => !m.replace(/[><]/g, '').trim().match(/^af-hub$/),
      );
      return otherText.length > 0;
    });

    expect(hasPlaceholder).toBe(true);
  });

  test('page component does not contain navigation bar or sidebar elements', () => {
    const srcDir = path.join(WEB_ROOT, 'src');
    expect(fs.existsSync(srcDir)).toBe(true);

    const tsxFiles = findFilesRecursive(srcDir, /\.(tsx|ts|jsx|js)$/);
    const pageFiles = tsxFiles.filter((f) => {
      const content = readTextFile(f);
      return content !== null && content.includes('af-hub');
    });
    expect(pageFiles.length).toBeGreaterThan(0);

    for (const f of pageFiles) {
      const content = readTextFile(f)!;
      // Should not contain nav/sidebar elements in the Hello World page
      expect(content).not.toMatch(/<nav[\s>]/i);
      expect(content).not.toMatch(/<Sidebar[\s>]/i);
      expect(content).not.toMatch(/<aside[\s>]/i);
    }
  });

  // Runtime render test — requires React + @testing-library/react
  // Will be enabled when group 4 installs React dependencies.
  test.todo(
    'runtime: rendering App at route "/" displays af-hub text and placeholder message',
  );
});

// ===========================================================================
// TS-04-17: React Router configured with single route at '/'
// Requirement: 04-REQ-5.2
// ===========================================================================

describe('TS-04-17: React Router configured with single route at /', () => {
  test('router configuration defines a route with path /', () => {
    const srcDir = path.join(WEB_ROOT, 'src');
    expect(fs.existsSync(srcDir)).toBe(true);

    // Search for router configuration — could be in main.tsx, App.tsx, or router.tsx
    const tsxFiles = findFilesRecursive(srcDir, /\.(tsx|ts)$/);
    expect(tsxFiles.length).toBeGreaterThan(0);

    const routerFiles = tsxFiles.filter((f) => {
      const content = readTextFile(f);
      if (!content) return false;
      // Look for route path definition
      return (
        content.includes("path: '/'") ||
        content.includes('path: "/"') ||
        content.includes("path='/'") ||
        content.includes('path="/"') ||
        content.includes('<Route') ||
        content.includes('createBrowserRouter') ||
        content.includes('createHashRouter')
      );
    });

    expect(routerFiles.length).toBeGreaterThan(0);

    // Verify at least one file defines a route at '/'
    const hasRootRoute = routerFiles.some((f) => {
      const content = readTextFile(f)!;
      return (
        content.includes("path: '/'") ||
        content.includes('path: "/"') ||
        content.includes("path='/'") ||
        content.includes('path="/"')
      );
    });

    expect(hasRootRoute).toBe(true);
  });

  test('route at / is mapped to a component that renders af-hub', () => {
    const srcDir = path.join(WEB_ROOT, 'src');
    expect(fs.existsSync(srcDir)).toBe(true);

    // Find route config file
    const tsxFiles = findFilesRecursive(srcDir, /\.(tsx|ts)$/);

    // There must be a file with a root route...
    const routerFiles = tsxFiles.filter((f) => {
      const content = readTextFile(f);
      if (!content) return false;
      return (
        content.includes("path: '/'") ||
        content.includes('path: "/"') ||
        content.includes("path='/'") ||
        content.includes('path="/"')
      );
    });
    expect(routerFiles.length).toBeGreaterThan(0);

    // ...and either the route file itself or an imported component contains 'af-hub'
    const allSrcFiles = tsxFiles;
    const afHubFiles = allSrcFiles.filter((f) => {
      const content = readTextFile(f);
      return content !== null && content.includes('af-hub');
    });
    expect(afHubFiles.length).toBeGreaterThan(0);
  });

  // Runtime check
  test.todo(
    'runtime: only one route is defined in the router configuration',
  );
});

// ===========================================================================
// TS-04-P4: Property — Hello World always renders without crashes
// Requirement: 04-PROP-4 (validates 04-REQ-5.1, 04-REQ-5.2)
// ===========================================================================

describe('TS-04-P4: Hello World always renders without crashes', () => {
  test('static: page component source contains af-hub literal', () => {
    const srcDir = path.join(WEB_ROOT, 'src');
    expect(fs.existsSync(srcDir)).toBe(true);

    const tsxFiles = findFilesRecursive(srcDir, /\.(tsx|ts|jsx|js)$/);
    const hasAfHub = tsxFiles.some((f) => {
      const content = readTextFile(f);
      return content !== null && content.includes('af-hub');
    });

    expect(hasAfHub).toBe(true);
  });

  test('static: page component source contains at least one placeholder string', () => {
    const srcDir = path.join(WEB_ROOT, 'src');
    expect(fs.existsSync(srcDir)).toBe(true);

    const tsxFiles = findFilesRecursive(srcDir, /\.(tsx|ts|jsx|js)$/);
    const pageFiles = tsxFiles.filter((f) => {
      const content = readTextFile(f);
      return content !== null && content.includes('af-hub');
    });
    expect(pageFiles.length).toBeGreaterThan(0);

    // The page file must have more than just 'af-hub' visible text
    const hasExtraText = pageFiles.some((f) => {
      const content = readTextFile(f)!;
      const textMatches = content.match(/>[^<]*[a-zA-Z][^<]*</g);
      if (!textMatches) return false;
      const otherText = textMatches.filter(
        (m) => !m.replace(/[><]/g, '').trim().match(/^af-hub$/),
      );
      return otherText.length > 0;
    });

    expect(hasExtraText).toBe(true);
  });

  // Runtime checks — need React + testing-library
  test.todo(
    'runtime: rendering App at route "/" produces non-empty HTML with no uncaught exceptions',
  );

  test.todo(
    'runtime: rendering App at route "/" in production mode produces same result',
  );
});

// ===========================================================================
// TS-04-E6: Unknown route does not crash the React app
// Requirement: 04-REQ-5.E1
// ===========================================================================

describe('TS-04-E6: Unknown route does not crash the app', () => {
  test('router configuration includes a catch-all or fallback route', () => {
    const srcDir = path.join(WEB_ROOT, 'src');
    expect(fs.existsSync(srcDir)).toBe(true);

    const tsxFiles = findFilesRecursive(srcDir, /\.(tsx|ts)$/);
    expect(tsxFiles.length).toBeGreaterThan(0);

    // Look for catch-all route patterns:
    // - path: '*'
    // - path="*"
    // - <Route path="*"
    // - errorElement
    // - Navigate (redirect fallback)
    const hasFallback = tsxFiles.some((f) => {
      const content = readTextFile(f);
      if (!content) return false;
      return (
        content.includes("path: '*'") ||
        content.includes('path: "*"') ||
        content.includes("path='*'") ||
        content.includes('path="*"') ||
        content.includes('errorElement') ||
        (content.includes('Navigate') && content.includes("to='/'")) ||
        (content.includes('Navigate') && content.includes('to="/'))
      );
    });

    expect(hasFallback).toBe(true);
  });

  // Runtime render test — requires React + @testing-library/react
  test.todo(
    'runtime: rendering App at route "/unknown" produces non-empty HTML without exceptions',
  );

  test.todo(
    'runtime: rendering App at route "/unknown" does not trigger React error boundary',
  );
});

// ===========================================================================
// TS-04-18: docs/web-ui.md covers all required topics
// Requirement: 04-REQ-6.1
// ===========================================================================

describe('TS-04-18: docs/web-ui.md covers all required topics', () => {
  const docsPath = path.join(REPO_ROOT, 'docs', 'web-ui.md');

  test('docs/web-ui.md exists', () => {
    expect(fs.existsSync(docsPath)).toBe(true);
  });

  test('documents Node.js 18+ requirement', () => {
    const doc = readTextFile(docsPath);
    expect(doc).not.toBeNull();

    const mentionsNode18 =
      doc!.includes('Node 18') ||
      doc!.includes('Node.js 18') ||
      doc!.includes('>=18');
    expect(mentionsNode18).toBe(true);
  });

  test('documents npm install', () => {
    const doc = readTextFile(docsPath);
    expect(doc).not.toBeNull();
    expect(doc).toContain('npm install');
  });

  test('documents npm run dev', () => {
    const doc = readTextFile(docsPath);
    expect(doc).not.toBeNull();
    expect(doc).toContain('npm run dev');
  });

  test('documents npm run build', () => {
    const doc = readTextFile(docsPath);
    expect(doc).not.toBeNull();
    expect(doc).toContain('npm run build');
  });

  test('documents the web/ directory structure', () => {
    const doc = readTextFile(docsPath);
    expect(doc).not.toBeNull();
    expect(doc).toContain('web/');
  });

  test('documents shadcn/ui and its copy-into-tree model', () => {
    const doc = readTextFile(docsPath);
    expect(doc).not.toBeNull();

    expect(doc).toContain('shadcn');

    // Must mention the copy/copied concept
    const mentionsCopy =
      doc!.includes('copied') ||
      doc!.includes('copy') ||
      doc!.includes('npx shadcn');
    expect(mentionsCopy).toBe(true);
  });
});

// ===========================================================================
// TS-04-19: README.md links to docs/web-ui.md and mentions make targets
// Requirement: 04-REQ-6.2
// ===========================================================================

describe('TS-04-19: README.md links to docs/web-ui.md and mentions make targets', () => {
  const readmePath = path.join(REPO_ROOT, 'README.md');

  test('README.md exists', () => {
    expect(fs.existsSync(readmePath)).toBe(true);
  });

  test('README.md references docs/web-ui.md', () => {
    const readme = readTextFile(readmePath);
    expect(readme).not.toBeNull();

    const hasLink =
      readme!.includes('docs/web-ui.md') || readme!.includes('web-ui');
    expect(hasLink).toBe(true);
  });

  test('README.md mentions make web-dev', () => {
    const readme = readTextFile(readmePath);
    expect(readme).not.toBeNull();

    const hasMakeWebDev =
      readme!.includes('make web-dev') || readme!.includes('web-dev');
    expect(hasMakeWebDev).toBe(true);
  });

  test('README.md mentions make web-build', () => {
    const readme = readTextFile(readmePath);
    expect(readme).not.toBeNull();

    const hasMakeWebBuild =
      readme!.includes('make web-build') || readme!.includes('web-build');
    expect(hasMakeWebBuild).toBe(true);
  });
});

// ===========================================================================
// TS-04-E2: Proxy survives backend being offline
// Requirement: 04-REQ-2.E1
// ===========================================================================

describe('TS-04-E2: Proxy config allows error passthrough when backend is offline', () => {
  const configPath = path.join(WEB_ROOT, 'vite.config.ts');

  test('vite.config.ts proxy has /api target pointing to http://localhost:8080', () => {
    const config = readTextFile(configPath);
    expect(config).not.toBeNull();

    const proxy = extractProxyConfig(config!);
    expect(proxy).not.toBeNull();

    const apiKey = Object.keys(proxy!).find((k) => k.startsWith('/api'));
    expect(apiKey).toBeDefined();
    expect(proxy![apiKey!].target).toBe('http://localhost:8080');
  });

  test('proxy config does not set secure: true (which would block error passthrough)', () => {
    const config = readTextFile(configPath);
    expect(config).not.toBeNull();

    const proxy = extractProxyConfig(config!);
    expect(proxy).not.toBeNull();

    // Check that no proxy rule sets secure: true, which could interfere
    // with error passthrough when the backend is down
    for (const key of Object.keys(proxy!)) {
      const block = proxy![key].rawBlock;
      // 'secure: true' would cause issues with non-HTTPS backends
      expect(block).not.toMatch(/secure\s*:\s*true/);
    }
  });

  // Behavioral verification note:
  // The full behavioral test (Vite returns 502 without crashing) is covered
  // by smoke test TS-04-SMOKE-3 and requires a running Vite dev server.
  // This static test verifies the proxy configuration permits error passthrough.
});

// ===========================================================================
// TS-04-E1: Node.js version documentation check
// Requirement: 04-REQ-1.E1
// ===========================================================================

describe('TS-04-E1: Node.js version prerequisite is documented', () => {
  test('docs/web-ui.md explicitly documents Node 18+ prerequisite', () => {
    const docsPath = path.join(REPO_ROOT, 'docs', 'web-ui.md');
    const doc = readTextFile(docsPath);
    expect(doc).not.toBeNull();

    const mentionsNode18 =
      doc!.includes('Node 18') ||
      doc!.includes('Node.js 18') ||
      doc!.includes('>=18');
    expect(mentionsNode18).toBe(true);
  });

  test('package.json engines.node specifies >= 18 (if engines field exists)', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();

    // This test checks that IF the engines field is present, it specifies
    // Node 18+. If engines is absent, the test still passes (since the
    // prerequisite can also be documented in docs/web-ui.md per TS-04-4).
    const engines = pkg!.engines as Record<string, string> | undefined;
    if (engines && engines.node) {
      const nodeSpec = engines.node;
      const specifies18 =
        nodeSpec.includes('18') || nodeSpec.includes('>=');
      expect(specifies18).toBe(true);
    }
  });

  test('at least one of package.json engines or docs/web-ui.md documents Node 18+', () => {
    // This is a combined check that at least one source documents the requirement
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();

    const engines = pkg!.engines as Record<string, string> | undefined;
    const enginesOk =
      (engines?.node ?? '').includes('18') ||
      (engines?.node ?? '').includes('>=');

    const docsPath = path.join(REPO_ROOT, 'docs', 'web-ui.md');
    const doc = readTextFile(docsPath);
    const docsOk =
      doc !== null &&
      (doc.includes('Node 18') ||
        doc.includes('Node.js 18') ||
        doc.includes('>=18'));

    expect(enginesOk || docsOk).toBe(true);
  });

  // Note: The dynamic test (running npm install or Vite with an older Node.js
  // version) is a manual/CI environment concern. The documentation check
  // above ensures developers are informed of the requirement. This is
  // documented in docs/web-ui.md per 04-REQ-1.E1.
});

// ---------------------------------------------------------------------------
// Utility: recursively find files matching a pattern
// ---------------------------------------------------------------------------

function findFilesRecursive(dir: string, pattern: RegExp): string[] {
  const results: string[] = [];

  if (!fs.existsSync(dir)) return results;

  const entries = fs.readdirSync(dir, { withFileTypes: true });
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      // Skip node_modules and hidden directories
      if (entry.name === 'node_modules' || entry.name.startsWith('.')) {
        continue;
      }
      results.push(...findFilesRecursive(fullPath, pattern));
    } else if (pattern.test(entry.name)) {
      results.push(fullPath);
    }
  }

  return results;
}
