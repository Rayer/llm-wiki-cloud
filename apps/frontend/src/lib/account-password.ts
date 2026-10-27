'use client';

import { responseError } from './auth-core';
import { getRuntimeConfig } from './runtime-config';
import { recordRequestResponse } from './request-ids';

export type PasswordSelfServiceStatus = { allowed: boolean; configured: boolean };
export type PasswordSelfServiceErrorCode = 'current_password_required' | 'current_password_invalid';

export class PasswordSelfServiceError extends Error {
  constructor(readonly code: PasswordSelfServiceErrorCode, message: string) {
    super(message);
    this.name = 'PasswordSelfServiceError';
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

async function passwordRequest(accessToken: string, method: 'GET' | 'POST', body?: unknown): Promise<unknown> {
  const response = await fetch(`${getRuntimeConfig().auth_url}/api/v1/auth/account/password`, {
    method,
    credentials: 'omit',
    cache: 'no-store',
    headers: {
      Authorization: `Bearer ${accessToken}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  recordRequestResponse(response);
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    if (isRecord(payload) && (payload.code === 'current_password_required' || payload.code === 'current_password_invalid')) {
      throw new PasswordSelfServiceError(payload.code, responseError(payload, `Auth request failed (${response.status})`));
    }
    throw new Error(responseError(payload, `Auth request failed (${response.status})`));
  }
  return payload;
}

export async function readPasswordSelfServiceStatus(accessToken: string): Promise<PasswordSelfServiceStatus> {
  const payload = await passwordRequest(accessToken, 'GET');
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('Auth response did not include password settings.');
  }
  const status = payload as Record<string, unknown>;
  if (typeof status.allowed !== 'boolean' || typeof status.configured !== 'boolean') {
    throw new Error('Auth response did not include password settings.');
  }
  return { allowed: status.allowed, configured: status.configured };
}

export async function changeAccountPassword(accessToken: string, newPassword: string, confirmPassword: string, currentPassword?: string): Promise<void> {
  await passwordRequest(accessToken, 'POST', {
    ...(currentPassword === undefined ? {} : { current_password: currentPassword }),
    new_password: newPassword,
    confirm_password: confirmPassword,
  });
}
