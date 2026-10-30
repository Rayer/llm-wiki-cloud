import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { ApiError } from '@/lib/api';

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
  mocks.discover.mockReset().mockResolvedValue({ ok: true, role: 'fast', status: 'success', partial: false, models: ['discovered-fast', 'discovered-heavy'] });
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
    expect(mocks.discover.mock.calls[0][0].credential_mode).toBe('replacement');
    expect(mocks.testMessage).not.toHaveBeenCalled();

    const fastModel = screen.getByLabelText('Fast model') as HTMLInputElement;
    fireEvent.change(fastModel, { target: { value: 'manual-fast-model' } });
    mocks.testMessage.mockResolvedValueOnce({ ok: true, role: 'fast', model: 'manual-fast-model', text: 'Fixture connection is ready.' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[0]);
    await screen.findByText('Fixture connection is ready.');
    expect(mocks.testMessage).toHaveBeenCalledWith(expect.objectContaining({ role: 'fast', models: { fast: 'manual-fast-model', heavy: '' } }));
    expect(screen.queryByText('Test Message succeeded.')).not.toBeNull();

    fireEvent.change(fastModel, { target: { value: 'edited-fast-model' } });
    expect(screen.queryByText('Fixture connection is ready.')).toBeNull();
    expect(screen.queryByText('Test Message succeeded.')).toBeNull();
    expect(document.querySelector('#byok-fast-models option[value="discovered-fast"]')).not.toBeNull();
  });

  it('keeps other role results and discovery when one model changes, and name edits preserve test results', async () => {
    render(<BYOKProfilesPanel />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Provider name' }), { target: { value: 'Fixture provider' } });
    fireEvent.change(screen.getByLabelText(/^API key/), { target: { value: 'synthetic-ui-fixture-key' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText('Found 2 models. Choose one or enter a model name manually.');
    fireEvent.change(screen.getByLabelText('Fast model'), { target: { value: 'fast-fixture' } });
    fireEvent.change(screen.getByLabelText('Heavy model'), { target: { value: 'heavy-fixture' } });
    mocks.testMessage.mockResolvedValueOnce({ ok: true, role: 'fast', model: 'fast-fixture', text: 'Fast role passed.' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[0]);
    await screen.findByText('Fast role passed.');
    mocks.testMessage.mockResolvedValueOnce({ ok: true, role: 'heavy', model: 'heavy-fixture', text: 'Heavy role passed.' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[1]);
    await screen.findByText('Heavy role passed.');

    fireEvent.change(screen.getByLabelText('Fast model'), { target: { value: 'fast-fixture-v2' } });
    expect(screen.queryByText('Fast role passed.')).toBeNull();
    expect(screen.queryByText('Heavy role passed.')).not.toBeNull();
    expect(document.querySelector('#byok-fast-models option[value="discovered-fast"]')).not.toBeNull();
    fireEvent.change(screen.getByRole('textbox', { name: 'Provider name' }), { target: { value: 'Renamed provider' } });
    expect(screen.queryByText('Heavy role passed.')).not.toBeNull();
  });

  it('sends credential mode none after Clear Key without including a hidden key', async () => {
    mocks.list.mockResolvedValueOnce([{
      id: 'profile-saved', name: 'Saved profile', provider: 'custom', protocol: 'openai_chat_completions',
      endpoint: 'https://fixture.example/v1', models: { fast: 'fast-fixture', heavy: 'heavy-fixture' },
      model: 'heavy-fixture', revision: 4, secret_configured: true,
    }]);
    render(<BYOKProfilesPanel />);
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }));
    fireEvent.click(screen.getByRole('checkbox', { name: /Clear saved key/ }));
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[0]);
    await screen.findByText('Fixture connection is ready.');
    const input = mocks.testMessage.mock.calls[0][0];
    expect(input.credential_mode).toBe('none');
    expect(input).not.toHaveProperty('api_key');
  });

  it('reports partial and unsupported discovery without blocking manual models or treating listing as a generation test', async () => {
    render(<BYOKProfilesPanel />);
    const model = await screen.findByLabelText('Fast model') as HTMLInputElement;
    fireEvent.change(model, { target: { value: 'manual-fixture-model' } });
    mocks.discover.mockResolvedValueOnce({ ok: true, role: 'fast', status: 'success', partial: true, models: ['listed-model'] });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText('Showing 1 models from a partial list. You can still enter any model name manually.');
    expect(model.value).toBe('manual-fixture-model');
    expect(mocks.testMessage).not.toHaveBeenCalled();

    mocks.discover.mockResolvedValueOnce({ ok: true, role: 'fast', status: 'unsupported', partial: false, models: [] });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText('This endpoint does not offer model discovery. Enter a model name manually; Test Message is still available.');
    expect(model.value).toBe('manual-fixture-model');
    expect(document.querySelector('#byok-fast-models option[value="listed-model"]')).not.toBeNull();
    expect(screen.getByText('The prior model list is retained but may be out of date.')).not.toBeNull();
    expect(mocks.testMessage).not.toHaveBeenCalled();
  });

  it.each([
    ['provider_discovery_unsupported', 'This endpoint does not offer model discovery. Enter a model name manually; Test Message is still available.'],
    ['provider_model_not_found', 'The provider could not find this model. Check the model name and access.'],
    ['provider_parameter_unsupported', 'The provider does not support a parameter sent with this request.'],
    ['provider_permission_denied', 'The credential does not have permission to use this provider operation.'],
  ])('shows the %s category and keeps manual model entry', async (code, message) => {
    mocks.discover.mockRejectedValueOnce(new ApiError(code, 502));
    render(<BYOKProfilesPanel />);
    const model = screen.getByLabelText('Fast model') as HTMLInputElement;
    fireEvent.change(model, { target: { value: 'manually-entered-model' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText(message);
    expect(model.value).toBe('manually-entered-model');
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
