import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
 getPublicConfig: vi.fn(), getAdminSettings: vi.fn(), updateAdminSettings: vi.fn(),
 startGoogleLogin: vi.fn(), signIn: vi.fn(), loginOpen: true,
}));
vi.mock('@/lib/api', async () => ({
 ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
 getPublicConfig: mocks.getPublicConfig, getAdminSettings: mocks.getAdminSettings,
 updateAdminSettings: mocks.updateAdminSettings, getAdminProjects: vi.fn().mockResolvedValue([]),
}));
vi.mock('@/lib/google-auth', () => ({ startGoogleLogin: mocks.startGoogleLogin }));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ hydrated: true, user: { role: 'admin' } }) }));
vi.mock('@/lib/i18n', () => ({ useLocale: () => ({ t: (key: string) => key }), useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ loginOpen: mocks.loginOpen, signIn: mocks.signIn, signInAsDemo: vi.fn() }) }));
vi.mock('@/components/RegisterModal', () => ({ RegisterModal: () => <div role="dialog" aria-label="email registration" /> }));
import { LoginModal } from '@/components/LoginModal';
import { AdminClient } from '@/components/AdminClient';
import { NavigationBlockerProvider } from '@/components/NavigationBlocker';

const settings = (email: boolean, google: boolean) => ({ registration_enabled: email && google, email_registration_enabled: email, google_registration_enabled: google, announcement_markdown: '' });
beforeEach(() => { vi.clearAllMocks(); mocks.loginOpen = true; mocks.signIn.mockResolvedValue(undefined); });
afterEach(cleanup);

describe('LWC-324 independent registration UI', () => {
 for (const email of [false, true]) for (const google of [false, true]) {
  it(`email=${email} Google=${google}: signup follows email, both existing login methods work`, async () => {
   mocks.getPublicConfig.mockResolvedValue(settings(email, google));
   render(<LoginModal />);
   await waitFor(() => expect(mocks.getPublicConfig).toHaveBeenCalled());
   const signup = screen.queryByRole('button', { name: 'Login.signUp' });
   expect(Boolean(signup)).toBe(email);
   if (signup) { fireEvent.click(signup); expect(screen.getByRole('dialog', { name: 'email registration' })).toBeDefined(); }
   fireEvent.change(screen.getByRole('textbox', { name: 'Login.email' }), { target: { value: 'existing@example.test' } });
   fireEvent.change(screen.getByLabelText('Login.password'), { target: { value: 'password123' } });
   fireEvent.click(screen.getByRole('button', { name: 'Login.signIn' }));
   await waitFor(() => expect(mocks.signIn).toHaveBeenCalledWith('existing@example.test', 'password123'));
   await waitFor(() => expect((screen.getByRole('button', { name: 'Login.continueWithGoogle' }) as HTMLButtonElement).disabled).toBe(false));
   fireEvent.click(screen.getByRole('button', { name: 'Login.continueWithGoogle' }));
   expect(mocks.startGoogleLogin).toHaveBeenCalledOnce();
  });
 }
 it('closes email signup on config failure while retaining Google and email login', async () => {
  mocks.getPublicConfig.mockRejectedValue(new Error('offline'));
  render(<LoginModal />);
  await waitFor(() => expect(mocks.getPublicConfig).toHaveBeenCalled());
  expect(screen.queryByRole('button', { name: 'Login.signUp' })).toBeNull();
  expect(screen.getByRole('button', { name: 'Login.signIn' })).toBeDefined();
  expect(screen.getByRole('button', { name: 'Login.continueWithGoogle' })).toBeDefined();
 });
 it('reopening login waits for fresh config instead of retaining old signup permission', async () => {
  mocks.getPublicConfig.mockResolvedValue(settings(true, true));
  const view = render(<LoginModal />);
  fireEvent.click(await screen.findByRole('button', { name: 'Login.signUp' }));
  expect(screen.getByRole('dialog', { name: 'email registration' })).toBeDefined();
  mocks.loginOpen = false; view.rerender(<LoginModal />);
  mocks.getPublicConfig.mockImplementation(() => new Promise(() => {}));
  mocks.loginOpen = true; view.rerender(<LoginModal />);
  expect(screen.queryByRole('button', { name: 'Login.signUp' })).toBeNull();
  expect(screen.queryByRole('dialog', { name: 'email registration' })).toBeNull();
 });
 it('loads and independently persists Admin toggles, then reloads the saved values', async () => {
  let saved = settings(false, true);
  mocks.getAdminSettings.mockImplementation(async () => saved);
  mocks.updateAdminSettings.mockImplementation(async (patch) => { saved = { ...saved, ...patch }; return saved; });
  const mount = async () => {
   const view = render(<NavigationBlockerProvider><AdminClient /></NavigationBlockerProvider>);
   fireEvent.click(await screen.findByRole('button', { name: 'Settings' }));
   await screen.findByRole('checkbox', { name: 'Admin.emailRegistrationEnabled' });
   return view;
  };
  const email = () => screen.getByRole('checkbox', { name: 'Admin.emailRegistrationEnabled' }) as HTMLInputElement;
  const google = () => screen.getByRole('checkbox', { name: 'Admin.googleRegistrationEnabled' }) as HTMLInputElement;
  const view = await mount();
  expect(email().checked).toBe(false); expect(google().checked).toBe(true);
  fireEvent.click(email());
  await waitFor(() => expect(email().checked).toBe(true));
  expect(mocks.updateAdminSettings).toHaveBeenLastCalledWith({ email_registration_enabled: true });
  expect(google().checked).toBe(true);
  fireEvent.click(google());
  await waitFor(() => expect(google().checked).toBe(false));
  expect(mocks.updateAdminSettings).toHaveBeenLastCalledWith({ google_registration_enabled: false });
  view.unmount(); await mount();
  expect(email().checked).toBe(true); expect(google().checked).toBe(false);
  mocks.updateAdminSettings.mockRejectedValue(new Error('save failed'));
  fireEvent.click(email());
  await screen.findByText('save failed');
  expect(email().checked).toBe(true); expect(google().checked).toBe(false);
 });
});
