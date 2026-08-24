import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { RegisterModal } from '@/components/RegisterModal';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const { mockRegister, mockUseWorkspace } = vi.hoisted(() => ({
  mockRegister: vi.fn(),
  mockUseWorkspace: vi.fn(),
}));

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: mockUseWorkspace }));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('UI/UX audit follow-up', () => {
  it('exposes the register dialog, Escape close, and password visibility toggle', async () => {
    mockUseWorkspace.mockReturnValue({ register: mockRegister });
    const onClose = vi.fn();
    render(<RegisterModal onClose={onClose} onSuccess={vi.fn()} t={(key) => key} />);
    expect(screen.getByRole('dialog', { name: 'Register.title' })).toBeDefined();
    const password = document.querySelector('input[autocomplete="new-password"]') as HTMLInputElement;
    expect(password.type).toBe('password');
    fireEvent.click(screen.getByRole('button', { name: 'Login.showPassword' }));
    expect(password.type).toBe('text');
    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });
});
