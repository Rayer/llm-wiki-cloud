import { CLIPairingClient } from '@/components/CLIPairingClient';

export default async function CLIPairingPage({
  searchParams,
}: {
  searchParams: Promise<{ user_code?: string | string[] }>;
}) {
  const params = await searchParams;
  const value = params.user_code;
  const initialUserCode = typeof value === 'string' ? value : '';
  return <CLIPairingClient initialUserCode={initialUserCode} />;
}
