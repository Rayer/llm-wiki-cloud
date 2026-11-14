import { StatusClient } from "@/components/StatusClient";

type StatusSearchParams = Record<string, string | string[] | undefined>;

export default async function StatusPage({
  searchParams,
}: {
  searchParams: Promise<StatusSearchParams>;
}) {
  const query = await searchParams;
  return (
    <StatusClient
      requestedProjectId={query.project_id}
      requestedExecutionId={query.execution_id}
    />
  );
}
