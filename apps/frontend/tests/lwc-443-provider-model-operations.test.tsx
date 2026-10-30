import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  list: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(), discover: vi.fn(), testMessage: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    listLLMProfiles: mocks.list,
    createLLMProfile: mocks.create,
    updateLLMProfile: mocks.update,
    deleteLLMProfile: mocks.remove,
    discoverLLMProfileModels: mocks.discover,
    testLLMProfileMessage: mocks.testMessage,
  };
});

import { BYOKProfilesPanel } from '@/components/BYOKProfilesPanel';

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.list.mockReset().mockResolvedValue([]);
  mocks.create.mockReset().mockResolvedValue({
    id: 'profile-fixture', name: 'Fixture', provider: 'openai', protocol: 'openai_chat_completions',
    endpoint: 'https://api.openai.com/v1', models: { fast: 'fast-fixture', heavy: 'heavy-fixture' },
    model: 'heavy-fixture', revision: 1, secret_configured: true,
  });
  mocks.update.mockReset();
  mocks.remove.mockReset();
  mocks.discover.mockReset().mockResolvedValue({ ok: true, role: 'fast', models: ['discovered-fast', 'discovered-heavy'] });
  mocks.testMessage.mockReset().mockResolvedValue({ ok: true, role: 'fast', model: 'custom-fast', text: 'Fixture connection is ready.' });
});

afterEach(() => cleanup());

describe('LWC-443 draft model operations', () => {
  it('discovers and tests a selected role independently, then invalidates results when the draft changes', async () => {
    render(<BYOKProfilesPanel />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Provider name' }), { target: { value: 'Fixture provider' } });
    fireEvent.change(screen.getByLabelText(/^API key/), { target: { value: 'synthetic-ui-fixture-key' } });

    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText('Found 2 models. Choose one or enter a model name manually.');
    expect(mocks.discover).toHaveBeenCalledWith(expect.objectContaining({ role: 'fast', models: { fast: '', heavy: '' }, api_key: 'synthetic-ui-fixture-key' }));
    expect(mocks.testMessage).not.toHaveBeenCalled();

    const fastModel = screen.getByLabelText('Fast model');
    fireEvent.change(fastModel, { target: { value: 'manual-fast-model' } });
    mocks.testMessage.mockResolvedValueOnce({ ok: true, role: 'fast', model: 'manual-fast-model', text: 'Fixture connection is ready.' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[0]);
    await screen.findByText('Fixture connection is ready.');
    expect(mocks.testMessage).toHaveBeenCalledWith(expect.objectContaining({ role: 'fast', models: { fast: 'manual-fast-model', heavy: '' } }));
    expect(screen.queryByText('Test Message succeeded.')).not.toBeNull();

    fireEvent.change(fastModel, { target: { value: 'edited-fast-model' } });
    expect(screen.queryByText('Fixture connection is ready.')).toBeNull();
    expect(screen.queryByText('Test Message succeeded.')).toBeNull();
  });

  it('saves role models without starting discovery or Test Message', async () => {
    const onProfileCreated = vi.fn();
    render(<BYOKProfilesPanel onProfileCreated={onProfileCreated} />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Provider name' }), { target: { value: 'Fixture provider' } });
    fireEvent.change(screen.getByLabelText('Fast model'), { target: { value: 'fast-fixture' } });
    fireEvent.change(screen.getByLabelText('Heavy model'), { target: { value: 'heavy-fixture' } });
    fireEvent.change(screen.getByLabelText(/^API key/), { target: { value: 'synthetic-ui-fixture-key' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }));

    await waitFor(() => expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({
      model: 'heavy-fixture', models: { fast: 'fast-fixture', heavy: 'heavy-fixture' },
    })));
    expect(mocks.discover).not.toHaveBeenCalled();
    expect(mocks.testMessage).not.toHaveBeenCalled();
    expect(onProfileCreated).toHaveBeenCalledTimes(1);
  });
});
