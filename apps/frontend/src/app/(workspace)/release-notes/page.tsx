import { ReleaseNotesReader } from '@/components/ReleaseNotesReader';

const productVersionPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

export default async function ReleaseNotesPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const params = await searchParams;
  const requested = params.version;
  const version = requested === undefined ? 'current'
    : typeof requested === 'string' && (requested === 'current' || productVersionPattern.test(requested))
      ? requested
      : 'invalid';
  return <ReleaseNotesReader key={version} presentation="page" initialVersion={version} />;
}
