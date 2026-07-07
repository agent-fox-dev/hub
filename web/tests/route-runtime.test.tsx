/**
 * Group 7 runtime render tests for the Hello World route.
 *
 * These tests use @testing-library/react with jsdom to verify that
 * the React components render correctly at different routes.
 *
 * Covers runtime assertions from:
 *   TS-04-16 (renders af-hub at /),
 *   TS-04-17 (single route at /),
 *   TS-04-P4 (always renders without crashes),
 *   TS-04-E6 (unknown route does not crash)
 *
 * Requirements: 04-REQ-5.1, 04-REQ-5.2, 04-REQ-5.E1, 04-PROP-4
 */
import { describe, test, expect, afterEach } from 'vitest';
import { render, cleanup, within } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import HomePage from '../src/pages/HomePage';
import NotFound from '../src/pages/NotFound';

// Clean up the DOM after each test to prevent cross-test leakage
afterEach(() => {
  cleanup();
});

/**
 * Wrap components in the same providers used by main.tsx,
 * using MemoryRouter for test-controlled routing.
 */
function renderWithProviders(initialRoute: string) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
    },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialRoute]}>
        <Routes>
          <Route path="/" element={<HomePage />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// ===========================================================================
// TS-04-16 (runtime): Hello World route renders 'af-hub' at '/'
// Requirement: 04-REQ-5.1
// ===========================================================================

describe('TS-04-16 runtime: Hello World route renders af-hub at /', () => {
  test('rendering App at route "/" displays af-hub text', () => {
    const { container } = renderWithProviders('/');
    expect(within(container).getByText('af-hub')).toBeInTheDocument();
  });

  test('rendering App at route "/" displays a placeholder message', () => {
    const { container } = renderWithProviders('/');
    const textContent = container.textContent ?? '';

    // Must contain 'af-hub' plus additional text
    expect(textContent).toContain('af-hub');
    // Remove 'af-hub' and check that meaningful text remains
    const remaining = textContent.replace(/af-hub/g, '').trim();
    expect(remaining.length).toBeGreaterThan(0);
  });
});

// ===========================================================================
// TS-04-17 (runtime): React Router has a single route at '/'
// Requirement: 04-REQ-5.2
// ===========================================================================

describe('TS-04-17 runtime: route at / renders the Hello World component', () => {
  test('navigating to "/" renders the HomePage component with af-hub', () => {
    const { container } = renderWithProviders('/');
    expect(within(container).getByText('af-hub')).toBeInTheDocument();
  });
});

// ===========================================================================
// TS-04-P4 (runtime): Hello World always renders without crashes
// Requirement: 04-PROP-4 (validates 04-REQ-5.1, 04-REQ-5.2)
// ===========================================================================

describe('TS-04-P4 runtime: Hello World renders without crashes', () => {
  test('rendering App at route "/" produces non-empty HTML with no uncaught exceptions', () => {
    const { container } = renderWithProviders('/');

    // Non-empty HTML
    expect(container.innerHTML).not.toBe('');
    // Contains the expected text
    expect(within(container).getByText('af-hub')).toBeInTheDocument();
  });

  test('rendering App at route "/" produces consistent output across renders', () => {
    // Verify the component renders the same content consistently
    // by rendering, capturing HTML, cleaning up, and rendering again.
    const { container: render1 } = renderWithProviders('/');
    const html1 = render1.innerHTML;
    cleanup();

    const { container: render2 } = renderWithProviders('/');
    const html2 = render2.innerHTML;

    expect(html1).toBe(html2);
    expect(html1).not.toBe('');
    expect(html1).toContain('af-hub');
  });
});

// ===========================================================================
// TS-04-E6 (runtime): Unknown route does not crash the app
// Requirement: 04-REQ-5.E1
// ===========================================================================

describe('TS-04-E6 runtime: unknown route does not crash the app', () => {
  test('rendering App at route "/unknown" produces non-empty HTML without exceptions', () => {
    const { container } = renderWithProviders('/unknown');

    // Must render something (non-blank page)
    expect(container.innerHTML).not.toBe('');
    expect(container.textContent).not.toBe('');
  });

  test('rendering App at route "/unknown" does not trigger React error boundary', () => {
    // If React error boundary were triggered, the render would throw or
    // produce an error message. We verify normal rendering occurs.
    const { container } = renderWithProviders('/unknown');

    // Should not contain common error boundary text
    expect(container.textContent).not.toContain('Something went wrong');
    expect(container.textContent).not.toContain('Error boundary');

    // Should contain the NotFound component's content
    expect(container.textContent).toContain('404');
  });
});
