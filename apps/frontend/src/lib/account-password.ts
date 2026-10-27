'use client';

import { responseError } from './auth-core';
import { getRuntimeConfig } from './runtime-config';
import { recordRequestResponse } from './request-ids';

export type PasswordSelfServiceStatus = { allowed: boolean; configured: boolean };
export type PasswordSelfServiceErrorCode = 'current_password_required' | 'current_password_invalid';
export type PasswordSelfServiceAuthContext = {
  readonly accessToken: string;
  refreshAccessToken: () => Promise<string | null>;
};

export class PasswordSelfServiceError extends Error {
  constructor(readonly code: PasswordSelfServiceErrorCode, message: string) {
    super(message);
    this.name = 'PasswordSelfServiceError';
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

async function passwordRequest(context: PasswordSelfServiceAuthContext, method: 'GET' | 'POST', body?: unknown): Promise<unknown> {
  const bodyText = body === undefined ? undefined : JSON.stringify(body);
  const makeRequest = async (accessToken: string) => {
    const response = await fetch(`${getRuntimeConfig().auth_url}/api/v1/auth/account/password`, {
      method,
      credentials: 'omit',
      cache: 'no-store',
      headers: {
        Authorization: `Bearer ${accessToken}`,
        ...(bodyText === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: bodyText,
    });
    recordRequestResponse(response);
    return response;
  };

  let response = await makeRequest(context.accessToken);
  if (response.status === 401) {
    const refreshed = await context.refreshAccessToken();
    if (refreshed) response = await makeRequest(refreshed);
  }
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    if (isRecord(payload) && (payload.code === 'current_password_required' || payload.code === 'current_password_invalid')) {
      throw new PasswordSelfServiceError(payload.code, responseError(payload, `Auth request failed (${response.status})`));
    }
    throw new Error(responseError(payload, `Auth request failed (${response.status})`));
  }
  return payload;
}

export async function readPasswordSelfServiceStatus(context: PasswordSelfServiceAuthContext): Promise<PasswordSelfServiceStatus> {
  const payload = await passwordRequest(context, 'GET');
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('Auth response did not include password settings.');
  }
  const status = payload as Record<string, unknown>;
  if (typeof status.allowed !== 'boolean' || typeof status.configured !== 'boolean') {
    throw new Error('Auth response did not include password settings.');
  }
  return { allowed: status.allowed, configured: status.configured };
}

export async function changeAccountPassword(context: PasswordSelfServiceAuthContext, newPassword: string, confirmPassword: string, currentPassword?: string): Promise<void> {
  await passwordRequest(context, 'POST', {
    ...(currentPassword === undefined ? {} : { current_password: currentPassword }),
    new_password: newPassword,
    confirm_password: confirmPassword,
  });
}
