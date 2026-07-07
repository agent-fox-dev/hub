/**
 * Group 2 spec tests: Vite proxy, Makefile targets, and NPM scripts.
 *
 * Covers: TS-04-6, TS-04-7, TS-04-8, TS-04-9, TS-04-P1,
 *         TS-04-10, TS-04-11, TS-04-P2,
 *         TS-04-12, TS-04-13, TS-04-14, TS-04-15,
 *         TS-04-E3, TS-04-E4, TS-04-E5, TS-04-P3
 * Requirements: 04-REQ-2.1 through 04-REQ-2.4,
 *               04-REQ-3.1, 04-REQ-3.2, 04-REQ-3.E1, 04-REQ-3.E2,
 *               04-REQ-4.1 through 04-REQ-4.4, 04-REQ-4.E1
 *
 * These tests validate the Vite proxy configuration, Makefile build
 * targets, NPM scripts, and exit-code propagation by reading config
 * files and performing static analysis. They run in Node.js environment.
 */
import { describe, test, expect, afterAll } from 'vitest';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import { execSync } from 'node:child_process';

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
  return JSON.parse(fs.readFileSync(filePath, 'utf-8')) as Record<string, unknown>;
}

// ---------------------------------------------------------------------------
// Proxy-config extraction helper
// ---------------------------------------------------------------------------

/**
 * Extract proxy configuration keys from vite.config.ts source text.
 *
 * This uses a simple regex approach to find proxy rule definitions.
 * It looks for string-literal keys (single- or double-quoted, or bare)
 * followed by an object containing `target`.
 *
 * Returns a map of path prefix -> { target, rawBlock } or null if
 * the file doesn't exist or has no proxy section.
 */
function extractProxyConfig(
  configSource: string,
): Record<string, { target: string; rawBlock: string }> | null {
  // Find the proxy section in the server config
  const proxyMatch = configSource.match(/proxy\s*:\s*\{/);
  if (!proxyMatch) return null;

  const result: Record<string, { target: string; rawBlock: string }> = {};

  // Match proxy rule entries: '/path': { ... target: '...' ... }
  // Handles both quoted and unquoted keys, single and double quotes
  const rulePattern =
    /['"]?(\/[a-zA-Z/*]+)['"]?\s*:\s*\{([^}]*)\}/g;
  let match: RegExpExecArray | null;

  while ((match = rulePattern.exec(configSource)) !== null) {
    const routePath = match[1];
    const block = match[2];

    // Extract target value from the block
    const targetMatch = block.match(
      /target\s*:\s*['"]([^'"]+)['"]/,
    );
    if (targetMatch) {
      result[routePath] = {
        target: targetMatch[1],
        rawBlock: block,
      };
    }
  }

  return Object.keys(result).length > 0 ? result : null;
}

/**
 * Extract a Makefile target body by name.
 *
 * Returns the recipe lines (indented with tab) for the given target,
 * or null if the target is not found.
 */
function extractMakefileTarget(
  makefileContent: string,
  targetName: string,
): string | null {
  // Match the target definition and its recipe lines (tab-indented)
  const lines = makefileContent.split('\n');
  let inTarget = false;
  const recipeLines: string[] = [];

  for (const line of lines) {
    // Target definition: "targetName:" at the start of a line
    if (
      line.match(
        new RegExp(`^${targetName}\\s*:`),
      )
    ) {
      inTarget = true;
      continue;
    }

    if (inTarget) {
      // Recipe lines start with a tab character
      if (line.startsWith('\t') || line.startsWith('  ')) {
        recipeLines.push(line);
      } else if (line.trim() === '') {
        // Empty lines can appear within a recipe
        recipeLines.push(line);
      } else {
        // Non-indented, non-empty line ends the recipe
        break;
      }
    }
  }

  return recipeLines.length > 0 ? recipeLines.join('\n') : null;
}

// ===========================================================================
// TS-04-6: Vite dev proxy forwards /api/* to http://localhost:8080
// Requirement: 04-REQ-2.1
// ===========================================================================

describe('TS-04-6: Vite dev proxy /api/* → http://localhost:8080', () => {
  const configPath = path.join(WEB_ROOT, 'vite.config.ts');

  test('vite.config.ts exists', () => {
    expect(fs.existsSync(configPath)).toBe(true);
  });

  test('vite.config.ts contains proxy configuration with localhost:8080', () => {
    const config = readTextFile(configPath);
    expect(config).not.toBeNull();
    expect(config).toContain('localhost:8080');
  });

  test('proxy has /api rule targeting http://localhost:8080', () => {
    const config = readTextFile(configPath);
    expect(config).not.toBeNull();
    expect(config).toContain('/api');

    const proxy = extractProxyConfig(config!);
    expect(proxy).not.toBeNull();

    // Find a rule matching /api (could be '/api' or '/api/')
    const apiKey = Object.keys(proxy!).find((k) => k.startsWith('/api'));
    expect(apiKey).toBeDefined();
    expect(proxy![apiKey!].target).toBe('http://localhost:8080');
  });

  test('/api proxy rule does NOT strip the /api prefix via rewrite', () => {
    const config = readTextFile(configPath);
    expect(config).not.toBeNull();

    const proxy = extractProxyConfig(config!);
    expect(proxy).not.toBeNull();

    const apiKey = Object.keys(proxy!).find((k) => k.startsWith('/api'));
    expect(apiKey).toBeDefined();

    // Check there's no rewrite that removes '/api'
    // Common pattern: rewrite: { '^/api': '' }
    const block = proxy![apiKey!].rawBlock;
    const stripRewrite = block.match(/rewrite.*['"].*['"]\s*:\s*['"]\s*['"]/);
    expect(stripRewrite).toBeNull();
  });
});

// ===========================================================================
// TS-04-7: Vite dev proxy forwards /healthz to http://localhost:8080
// Requirement: 04-REQ-2.2
// ===========================================================================

describe('TS-04-7: Vite dev proxy /healthz → http://localhost:8080', () => {
  test('proxy has /healthz rule targeting http://localhost:8080', () => {
    const config = readTextFile(path.join(WEB_ROOT, 'vite.config.ts'));
    expect(config).not.toBeNull();

    const proxy = extractProxyConfig(config!);
    expect(proxy).not.toBeNull();
    expect(proxy).toHaveProperty('/healthz');
    expect(proxy!['/healthz'].target).toBe('http://localhost:8080');
  });
});

// ===========================================================================
// TS-04-8: Vite dev proxy forwards /readyz to http://localhost:8080
// Requirement: 04-REQ-2.3
// ===========================================================================

describe('TS-04-8: Vite dev proxy /readyz → http://localhost:8080', () => {
  test('proxy has /readyz rule targeting http://localhost:8080', () => {
    const config = readTextFile(path.join(WEB_ROOT, 'vite.config.ts'));
    expect(config).not.toBeNull();

    const proxy = extractProxyConfig(config!);
    expect(proxy).not.toBeNull();
    expect(proxy).toHaveProperty('/readyz');
    expect(proxy!['/readyz'].target).toBe('http://localhost:8080');
  });
});

// ===========================================================================
// TS-04-9: vite.config.ts defines all three proxy rules targeting port 8080
// Requirement: 04-REQ-2.4
// ===========================================================================

describe('TS-04-9: All three proxy rules present targeting port 8080', () => {
  test('proxy section contains /api, /healthz, and /readyz all targeting localhost:8080', () => {
    const config = readTextFile(path.join(WEB_ROOT, 'vite.config.ts'));
    expect(config).not.toBeNull();

    const proxy = extractProxyConfig(config!);
    expect(proxy).not.toBeNull();

    // /api (or key starting with /api)
    const apiKey = Object.keys(proxy!).find((k) => k.startsWith('/api'));
    expect(apiKey).toBeDefined();
    expect(proxy![apiKey!].target).toBe('http://localhost:8080');

    // /healthz
    expect(proxy).toHaveProperty('/healthz');
    expect(proxy!['/healthz'].target).toBe('http://localhost:8080');

    // /readyz
    expect(proxy).toHaveProperty('/readyz');
    expect(proxy!['/readyz'].target).toBe('http://localhost:8080');
  });
});

// ===========================================================================
// TS-04-P1: Property — proxy rules cover all required backend endpoints
// Requirement: 04-PROP-1 (validates 04-REQ-2.1–2.4)
// ===========================================================================

describe('TS-04-P1: Proxy rules cover all required backend endpoints', () => {
  const samplePaths = [
    '/api/anything',
    '/api/users',
    '/api/v1/resource',
    '/healthz',
    '/readyz',
  ];

  test.each(samplePaths)(
    'proxy has a matching rule for %s targeting http://localhost:8080',
    (samplePath) => {
      const config = readTextFile(path.join(WEB_ROOT, 'vite.config.ts'));
      expect(config).not.toBeNull();

      const proxy = extractProxyConfig(config!);
      expect(proxy).not.toBeNull();

      // Find a proxy rule key that the sample path starts with
      const matchingKey = Object.keys(proxy!).find((key) => {
        // Remove trailing wildcard for matching
        const prefix = key.replace(/\/?\*$/, '');
        return samplePath.startsWith(prefix);
      });

      expect(matchingKey).toBeDefined();
      expect(proxy![matchingKey!].target).toBe('http://localhost:8080');
    },
  );
});

// ===========================================================================
// TS-04-10: make web-dev installs deps when node_modules/ absent, then
//           starts the Vite dev server
// Requirement: 04-REQ-3.1
// ===========================================================================

describe('TS-04-10: make web-dev target installs deps then starts dev server', () => {
  const makefilePath = path.join(REPO_ROOT, 'Makefile');

  test('Makefile exists at the repo root', () => {
    expect(fs.existsSync(makefilePath)).toBe(true);
  });

  test('Makefile has a web-dev target', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-dev');
    expect(target).not.toBeNull();
  });

  test('web-dev target contains a node_modules check that triggers npm install', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-dev');
    expect(target).not.toBeNull();

    // Must reference node_modules and npm install
    expect(target).toContain('node_modules');
    expect(target).toMatch(/npm\s+install/);
  });

  test('web-dev target runs npm run dev', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-dev');
    expect(target).not.toBeNull();
    expect(target).toMatch(/npm\s+run\s+dev/);
  });

  test('npm install appears before npm run dev in web-dev target', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-dev');
    expect(target).not.toBeNull();

    const installPos = target!.search(/npm\s+install/);
    const runDevPos = target!.search(/npm\s+run\s+dev/);

    expect(installPos).toBeGreaterThanOrEqual(0);
    expect(runDevPos).toBeGreaterThanOrEqual(0);
    expect(installPos).toBeLessThan(runDevPos);
  });
});

// ===========================================================================
// TS-04-11: make web-build installs deps when node_modules/ absent, then
//           runs the production build
// Requirement: 04-REQ-3.2
// ===========================================================================

describe('TS-04-11: make web-build target installs deps then builds', () => {
  const makefilePath = path.join(REPO_ROOT, 'Makefile');

  test('Makefile has a web-build target', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-build');
    expect(target).not.toBeNull();
  });

  test('web-build target contains a node_modules check that triggers npm install', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-build');
    expect(target).not.toBeNull();

    expect(target).toContain('node_modules');
    expect(target).toMatch(/npm\s+install/);
  });

  test('web-build target runs npm run build', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-build');
    expect(target).not.toBeNull();
    expect(target).toMatch(/npm\s+run\s+build/);
  });

  test('npm install appears before npm run build in web-build target', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-build');
    expect(target).not.toBeNull();

    const installPos = target!.search(/npm\s+install/);
    const runBuildPos = target!.search(/npm\s+run\s+build/);

    expect(installPos).toBeGreaterThanOrEqual(0);
    expect(runBuildPos).toBeGreaterThanOrEqual(0);
    expect(installPos).toBeLessThan(runBuildPos);
  });
});

// ===========================================================================
// TS-04-P2: Property — Makefile targets always install deps before running
// Requirement: 04-PROP-2 (validates 04-REQ-3.1, 04-REQ-3.2)
// ===========================================================================

describe('TS-04-P2: Makefile targets always install deps before running', () => {
  const makefilePath = path.join(REPO_ROOT, 'Makefile');

  test.each(['web-dev', 'web-build'])(
    '%s: npm install occurs at most once (no retry loop)',
    (targetName) => {
      const content = readTextFile(makefilePath);
      expect(content).not.toBeNull();

      const target = extractMakefileTarget(content!, targetName);
      expect(target).not.toBeNull();

      // Count occurrences of 'npm install' — must be <= 1
      const installMatches = target!.match(/npm\s+install/g);
      expect(installMatches).not.toBeNull();
      expect(installMatches!.length).toBeLessThanOrEqual(1);
    },
  );

  test.each(['web-dev', 'web-build'])(
    '%s: install always precedes run step',
    (targetName) => {
      const content = readTextFile(makefilePath);
      expect(content).not.toBeNull();

      const target = extractMakefileTarget(content!, targetName);
      expect(target).not.toBeNull();

      const installPos = target!.search(/npm\s+install/);
      const runPos = target!.search(/npm\s+run/);

      expect(installPos).toBeGreaterThanOrEqual(0);
      expect(runPos).toBeGreaterThanOrEqual(0);
      expect(installPos).toBeLessThan(runPos);
    },
  );

  test.each(['web-dev', 'web-build'])(
    '%s: node_modules check gates the install',
    (targetName) => {
      const content = readTextFile(makefilePath);
      expect(content).not.toBeNull();

      const target = extractMakefileTarget(content!, targetName);
      expect(target).not.toBeNull();

      expect(target).toContain('node_modules');
    },
  );
});

// ===========================================================================
// TS-04-12: npm run dev starts the Vite dev server with HMR
// Requirement: 04-REQ-4.1
// ===========================================================================

describe('TS-04-12: npm run dev starts the Vite dev server with HMR', () => {
  test('package.json has a dev script', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string> | undefined;
    expect(scripts).toBeDefined();
    expect(scripts).toHaveProperty('dev');
  });

  test('dev script invokes vite', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string>;
    expect(scripts.dev).toContain('vite');
  });

  test('dev script does not disable HMR', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string>;
    const devCmd = scripts.dev;
    // Should not contain HMR-disabling flags
    expect(devCmd).not.toContain('--no-hmr');
    expect(devCmd).not.toMatch(/hmr\s*:\s*false/);
  });
});

// ===========================================================================
// TS-04-13: npm run build runs Vite production build → web/dist/
// Requirement: 04-REQ-4.2
// ===========================================================================

describe('TS-04-13: npm run build runs Vite production build', () => {
  test('package.json has a build script', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string> | undefined;
    expect(scripts).toBeDefined();
    expect(scripts).toHaveProperty('build');
  });

  test('build script contains vite build', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string>;
    const buildCmd = scripts.build;
    // May be 'vite build' or 'tsc && vite build' or 'tsc -b && vite build'
    expect(buildCmd).toContain('vite build');
  });
});

// ===========================================================================
// TS-04-14: npm run lint runs ESLint and TypeScript type checking
// Requirement: 04-REQ-4.3
// ===========================================================================

describe('TS-04-14: npm run lint runs ESLint and TypeScript type checking', () => {
  test('package.json has a lint script', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string> | undefined;
    expect(scripts).toBeDefined();
    expect(scripts).toHaveProperty('lint');
  });

  test('lint script contains eslint', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string>;
    expect(scripts.lint).toContain('eslint');
  });

  test('lint script contains tsc with --noEmit', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string>;
    const lintCmd = scripts.lint;
    expect(lintCmd).toContain('tsc');
    expect(lintCmd).toContain('--noEmit');
  });
});

// ===========================================================================
// TS-04-15: npm run preview serves the web/dist/ output
// Requirement: 04-REQ-4.4
// ===========================================================================

describe('TS-04-15: npm run preview serves the production build', () => {
  test('package.json has a preview script', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string> | undefined;
    expect(scripts).toBeDefined();
    expect(scripts).toHaveProperty('preview');
  });

  test('preview script contains vite preview', () => {
    const pkg = readJsonFile(path.join(WEB_ROOT, 'package.json'));
    expect(pkg).not.toBeNull();
    const scripts = pkg!.scripts as Record<string, string>;
    expect(scripts.preview).toContain('vite preview');
  });
});

// ===========================================================================
// TS-04-E3: make web-build propagates non-zero exit codes on build failure
// Requirement: 04-REQ-3.E1
// ===========================================================================

describe('TS-04-E3: make web-build propagates non-zero exit codes', () => {
  const makefilePath = path.join(REPO_ROOT, 'Makefile');

  test('web-build target does not use dash prefix to ignore errors', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-build');
    expect(target).not.toBeNull();

    // In Makefile, a leading '-' before a command tells make to ignore errors.
    // None of the recipe lines should use this for npm commands.
    const lines = target!.split('\n').filter((l) => l.trim().length > 0);
    for (const line of lines) {
      const trimmed = line.replace(/^\t/, '');
      if (trimmed.match(/npm/)) {
        // Must not start with '-' (ignoring @ which suppresses echo)
        const withoutAt = trimmed.replace(/^@/, '');
        expect(withoutAt).not.toMatch(/^-\s/);
      }
    }
  });

  test('web-build target does not force exit 0 after npm commands', () => {
    const content = readTextFile(makefilePath);
    expect(content).not.toBeNull();

    const target = extractMakefileTarget(content!, 'web-build');
    expect(target).not.toBeNull();

    // Should not contain "exit 0" or "|| true" after npm commands
    expect(target).not.toMatch(/npm.*;\s*exit\s+0/);
    expect(target).not.toMatch(/npm.*\|\|\s*true/);
  });
});

// ===========================================================================
// TS-04-E4: make web-dev and web-build propagate npm install failure
// Requirement: 04-REQ-3.E2
// ===========================================================================

describe('TS-04-E4: Makefile targets propagate npm install failure', () => {
  const makefilePath = path.join(REPO_ROOT, 'Makefile');

  test.each(['web-dev', 'web-build'])(
    '%s: install step comes before run step',
    (targetName) => {
      const content = readTextFile(makefilePath);
      expect(content).not.toBeNull();

      const target = extractMakefileTarget(content!, targetName);
      expect(target).not.toBeNull();

      const installPos = target!.search(/npm\s+install/);
      const runPos = target!.search(/npm\s+run/);

      expect(installPos).toBeGreaterThanOrEqual(0);
      expect(runPos).toBeGreaterThanOrEqual(0);
      expect(installPos).toBeLessThan(runPos);
    },
  );

  test.each(['web-dev', 'web-build'])(
    '%s: does not suppress npm install exit code',
    (targetName) => {
      const content = readTextFile(makefilePath);
      expect(content).not.toBeNull();

      const target = extractMakefileTarget(content!, targetName);
      expect(target).not.toBeNull();

      const lines = target!.split('\n').filter((l) => l.trim().length > 0);
      for (const line of lines) {
        const trimmed = line.replace(/^\t/, '');
        if (trimmed.match(/npm\s+install/)) {
          // Must not start with '-' (ignoring @ which suppresses echo)
          const withoutAt = trimmed.replace(/^@/, '');
          expect(withoutAt).not.toMatch(/^-\s/);
        }
      }

      // Should not force success after npm install
      expect(target).not.toMatch(/npm\s+install.*;\s*exit\s+0/);
      expect(target).not.toMatch(/npm\s+install.*\|\|\s*true/);
    },
  );
});

// ===========================================================================
// TS-04-E5: npm run lint reports errors with file names and line numbers
// Requirement: 04-REQ-4.E1
// ===========================================================================

describe('TS-04-E5: npm run lint reports errors with file/line info', () => {
  const errorFilePath = path.join(WEB_ROOT, 'src', '__test_error__.tsx');

  afterAll(() => {
    // Clean up the temp error file if it exists
    if (fs.existsSync(errorFilePath)) {
      fs.unlinkSync(errorFilePath);
    }
  });

  test('npm run lint exits non-zero when a TypeScript error is present', () => {
    // Skip if src/ directory doesn't exist yet (pre-implementation)
    const srcDir = path.join(WEB_ROOT, 'src');
    if (!fs.existsSync(srcDir)) {
      // Before implementation, this test should still fail to confirm red state.
      // The src/ dir won't exist before group 4 scaffolds it, so we assert it
      // must exist for the lint test to be meaningful.
      expect(fs.existsSync(srcDir)).toBe(true);
      return;
    }

    // Create a deliberate TypeScript error
    fs.writeFileSync(
      errorFilePath,
      'const x: number = "not a number";\nexport default x;\n',
    );

    try {
      // Run lint and expect it to fail
      execSync('npm run lint', {
        cwd: WEB_ROOT,
        encoding: 'utf-8',
        stdio: ['pipe', 'pipe', 'pipe'],
      });
      // If we get here, lint didn't catch the error — fail
      expect(true).toBe(false); // should not reach here
    } catch (err: unknown) {
      const error = err as { status: number; stdout: string; stderr: string };
      expect(error.status).not.toBe(0);

      // Output should reference the error file
      const output = (error.stdout || '') + (error.stderr || '');
      expect(output).toContain('__test_error__');
    } finally {
      // Clean up
      if (fs.existsSync(errorFilePath)) {
        fs.unlinkSync(errorFilePath);
      }
    }
  });
});

// ===========================================================================
// TS-04-P3: Property — npm script exit codes propagated to calling process
// Requirement: 04-PROP-3 (validates 04-REQ-4.2, 04-REQ-4.3, 04-REQ-3.E1,
//              04-REQ-3.E2)
// ===========================================================================

describe('TS-04-P3: Exit codes are propagated without being masked', () => {
  const makefilePath = path.join(REPO_ROOT, 'Makefile');

  test.each(['web-dev', 'web-build'])(
    '%s: no recipe line suppresses errors via dash prefix',
    (targetName) => {
      const content = readTextFile(makefilePath);
      expect(content).not.toBeNull();

      const target = extractMakefileTarget(content!, targetName);
      expect(target).not.toBeNull();

      // Check no line uses the error-ignoring '- ' prefix for npm commands
      const lines = target!.split('\n').filter((l) => l.trim().length > 0);
      for (const line of lines) {
        const trimmed = line.replace(/^\t/, '');
        if (trimmed.match(/npm/)) {
          const withoutAt = trimmed.replace(/^@/, '');
          expect(withoutAt).not.toMatch(/^-\s/);
        }
      }
    },
  );

  test.each(['web-dev', 'web-build'])(
    '%s: no forced exit 0 after npm commands',
    (targetName) => {
      const content = readTextFile(makefilePath);
      expect(content).not.toBeNull();

      const target = extractMakefileTarget(content!, targetName);
      expect(target).not.toBeNull();

      expect(target).not.toMatch(/exit\s+0/);
    },
  );
});
