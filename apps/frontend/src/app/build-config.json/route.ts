import { API_URL, AUTH_URL } from '../../lib/public-build-config';

// Emit actual build-time values; never read deployment-time desired configuration.
export const dynamic = 'force-static';
export const revalidate = false;

export function GET() {
  return Response.json({ schema_version: 1, api_url: API_URL, auth_url: AUTH_URL });
}
