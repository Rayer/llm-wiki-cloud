import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ loadManifest: vi.fn(), writeText: vi.fn() }));

vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: { id: 'project-123', name: 'Current Project' } }) }));
vi.mock('@/lib/runtime-config', () => ({ getRuntimeConfig: () => ({ auth_url: 'https://auth.dev.test' }) }));
vi.mock('@/lib/cli-release-metadata', () => ({ loadCLIReleaseManifest: mocks.loadManifest }));

import { CLILaneClient } from '@/components/CLILaneClient';

const { cleanup, fireEvent, render, screen } = await import('@testing-library/react');

beforeEach(() => {
  mocks.loadManifest.mockReset().mockResolvedValue(null);
  mocks.writeText.mockReset().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: mocks.writeText } });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('CLI onboarding lane', () => {
  it('starts with four closed cards and opens only the selected card', () => {
    render(<CLILaneClient />);
    const cards = screen.getAllByRole('button').filter((button) => button.hasAttribute('aria-expanded'));
    expect(cards).toHaveLength(4);
    expect(cards.every((button) => button.getAttribute('aria-expanded') === 'false')).toBe(true);

    fireEvent.click(cards[0]);
    expect(cards[0].getAttribute('aria-expanded')).toBe('true');
    fireEvent.click(cards[1]);
    expect(cards[0].getAttribute('aria-expanded')).toBe('false');
    expect(cards[1].getAttribute('aria-expanded')).toBe('true');
    fireEvent.click(cards[1]);
    expect(cards.every((button) => button.getAttribute('aria-expanded') === 'false')).toBe(true);
  });

  it('keeps metadata failure safe and disables root commands until a path is entered', async () => {
    render(<CLILaneClient />);
    expect(screen.getByText('CLILane.agentNote')).toBeTruthy();
    const cards = screen.getAllByRole('button').filter((button) => button.hasAttribute('aria-expanded'));
    fireEvent.click(cards[0]);
    expect(await screen.findAllByText('CLILane.unavailable')).toHaveLength(4);
    expect(screen.queryAllByRole('link')).toHaveLength(0);

    fireEvent.click(cards[2]);
    expect(screen.getByText('CLILane.recoveryGuidance')).toBeTruthy();
    const rootInput = screen.getByLabelText('CLILane.rootLabel');
    fireEvent.click(screen.getByRole('button', { name: 'CLILane.copy' }));
    expect(mocks.writeText).not.toHaveBeenCalled();
    fireEvent.change(rootInput, { target: { value: "/Users/a user's Wiki 東京" } });
    const initCommand = screen.getByText(/\.\/lwc-sync init --vault/);
    expect(initCommand.textContent).toContain("'\\''");
    fireEvent.click(screen.getByRole('button', { name: 'CLILane.documentsExample' }));
    expect((rootInput as HTMLInputElement).value).toBe('/Users/your-name/Documents/YourWiki');
    expect(cards[2].getAttribute('aria-expanded')).toBe('true');
  });
});
