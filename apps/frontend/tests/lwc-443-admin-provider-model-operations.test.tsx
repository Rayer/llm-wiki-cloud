import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { ApiError } from '@/lib/api';

const mocks = vi.hoisted(() => ({
  list: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(), discover: vi.fn(), testMessage: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    listAdminLLMProfiles: mocks.list,
    createAdminLLMProfile: mocks.create,
    updateAdminLLMProfile: mocks.update,
    deleteAdminLLMProfile: mocks.remove,
    discoverAdminLLMProfileModels: mocks.discover,
    testAdminLLMProfileMessage: mocks.testMessage,
  };
});

import { AdminSharedProfilesPanel } from '@/components/AdminSharedProfilesPanel';

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.list.mockReset().mockResolvedValue({ profiles: [], deployment_credentials: [] });
  mocks.create.mockReset();
  mocks.update.mockReset();
  mocks.remove.mockReset();
  mocks.discover.mockReset().mockResolvedValue({ ok: true, role: 'fast', status: 'success', partial: false, models: ['discovered-fast'] });
  mocks.testMessage.mockReset().mockResolvedValue({ ok: true, role: 'fast', model: 'fixture-model', text: 'Admin fixture passed.' });
});

afterEach(() => cleanup());

describe('Admin shared profile operation feedback', () => {
  it('uses explicit replacement mode and preserves independent role results across model and name edits', async () => {
    render(<AdminSharedProfilesPanel />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Provider name' }), { target: { value: 'Fixture shared' } });
    fireEvent.change(screen.getByLabelText(/^API key/), { target: { value: 'synthetic-admin-fixture-key' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText('Found 1 models. Choose one or enter a model name manually.');
    expect(mocks.discover.mock.calls[0][0].credential_mode).toBe('replacement');
    fireEvent.change(screen.getByLabelText('Fast model'), { target: { value: 'fast-fixture' } });
    fireEvent.change(screen.getByLabelText('Heavy model'), { target: { value: 'heavy-fixture' } });

    mocks.testMessage.mockResolvedValueOnce({ ok: true, role: 'fast', model: 'fast-fixture', text: 'Fast admin passed.' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[0]);
    await screen.findByText('Fast admin passed.');
    mocks.testMessage.mockResolvedValueOnce({ ok: true, role: 'heavy', model: 'heavy-fixture', text: 'Heavy admin passed.' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[1]);
    await screen.findByText('Heavy admin passed.');

    fireEvent.change(screen.getByLabelText('Fast model'), { target: { value: 'fast-fixture-v2' } });
    expect(screen.queryByText('Fast admin passed.')).toBeNull();
    expect(screen.queryByText('Heavy admin passed.')).not.toBeNull();
    expect(document.querySelector('#admin-fast-models option[value="discovered-fast"]')).not.toBeNull();
    fireEvent.change(screen.getByRole('textbox', { name: 'Provider name' }), { target: { value: 'Renamed shared' } });
    expect(screen.queryByText('Heavy admin passed.')).not.toBeNull();
  });

  it('clears both role tests and discovery when a connection field changes', async () => {
    render(<AdminSharedProfilesPanel />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Provider name' }), { target: { value: 'Fixture shared' } });
    fireEvent.change(screen.getByLabelText(/^API key/), { target: { value: 'synthetic-admin-fixture-key' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText('Found 1 models. Choose one or enter a model name manually.');
    fireEvent.change(screen.getByLabelText('Fast model'), { target: { value: 'fast-fixture' } });
    fireEvent.change(screen.getByLabelText('Heavy model'), { target: { value: 'heavy-fixture' } });
    mocks.testMessage.mockResolvedValueOnce({ ok: true, role: 'fast', model: 'fast-fixture', text: 'Fast admin passed.' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[0]);
    await screen.findByText('Fast admin passed.');
    mocks.testMessage.mockResolvedValueOnce({ ok: true, role: 'heavy', model: 'heavy-fixture', text: 'Heavy admin passed.' });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[1]);
    await screen.findByText('Heavy admin passed.');

    fireEvent.change(screen.getByLabelText(/^API key/), { target: { value: 'synthetic-admin-fixture-key-v2' } });
    expect(screen.queryByText('Fast admin passed.')).toBeNull();
    expect(screen.queryByText('Heavy admin passed.')).toBeNull();
    expect(document.querySelector('#admin-fast-models option[value="discovered-fast"]')).toBeNull();
  });

  it('reports partial and unsupported discovery while keeping the manual model available', async () => {
    render(<AdminSharedProfilesPanel />);
    const model = await screen.findByLabelText('Fast model') as HTMLInputElement;
    fireEvent.change(model, { target: { value: 'manual-admin-fixture-model' } });
    mocks.discover.mockResolvedValueOnce({ ok: true, role: 'fast', status: 'success', partial: true, models: ['listed-admin-model'] });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText('Showing 1 models from a partial list. You can still enter any model name manually.');
    expect(model.value).toBe('manual-admin-fixture-model');

    mocks.discover.mockResolvedValueOnce({ ok: true, role: 'fast', status: 'unsupported', partial: false, models: [] });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText('This endpoint does not offer model discovery. Enter a model name manually; Test Message is still available.');
    expect(model.value).toBe('manual-admin-fixture-model');
    expect(document.querySelector('#admin-fast-models option[value="listed-admin-model"]')).not.toBeNull();
    expect(screen.getByText('The prior model list is retained but may be out of date.')).not.toBeNull();
  });

  it.each([
    ['provider_discovery_unsupported', 'This endpoint does not offer model discovery. Enter a model name manually; Test Message is still available.'],
    ['provider_model_not_found', 'The provider could not find this model. Check the model name and access.'],
    ['provider_parameter_unsupported', 'The provider does not support a parameter sent with this request.'],
    ['provider_permission_denied', 'The credential does not have permission to use this provider operation.'],
  ])('shows the %s category and keeps manual model entry', async (code, message) => {
    mocks.discover.mockRejectedValueOnce(new ApiError(code, 502));
    render(<AdminSharedProfilesPanel />);
    const model = screen.getByLabelText('Fast model') as HTMLInputElement;
    fireEvent.change(model, { target: { value: 'manual-admin-model' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Discover models' })[0]);
    await screen.findByText(message);
    expect(model.value).toBe('manual-admin-model');
  });
});
