import { CONFIG_URL } from '../../lib/public-build-config';

// Emit actual build-time values; never read deployment-time desired configuration.
export const dynamic = 'force-static';
export const revalidate = false;

export function GET() {
  return Response.json({ schema_version: 1, config_url: CONFIG_URL });
}
