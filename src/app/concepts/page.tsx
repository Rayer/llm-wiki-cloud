"use client";

import { ListClient } from "@/components/ListClient";
import { getConcepts } from "@/lib/api";
import { useT } from "@/lib/i18n";

export default function ConceptsPage() {
  const { t } = useT();

  return (
    <ListClient
      title={t('List.entriesTitle')}
      description={t('List.entriesDescription')}
      load={getConcepts}
      basePath="/concepts"
      entryType="concept"
    />
  );
}
