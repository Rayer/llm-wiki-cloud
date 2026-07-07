"use client";

import { ListClient } from "@/components/ListClient";
import { getSources } from "@/lib/api";

export default function SourcesPage() {
  return (
    <ListClient
      title="Sources"
      description="Original wiki source documents available from the LLM Wiki BFF."
      load={getSources}
      basePath="/sources"
      entryType="source"
    />
  );
}
