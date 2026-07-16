"use client";

import { ListClient } from "@/components/ListClient";
import { getSources } from "@/lib/api";
import { useT } from "@/lib/i18n";

export default function SourcesPage() {
  const { t } = useT();

  return (
    <ListClient
      title={t('List.sourcesTitle')}
      description={t('List.sourcesDescription')}
      load={getSources}
      basePath="/sources"
      entryType="source"
    />
  );
}
