"use client";

import { use } from "react";
import { DetailClient } from "@/components/DetailClient";
import { getSource } from "@/lib/api";

export default function SourceDetailPage({
  params,
}: {
  params: Promise<{ slug: string }>;
}) {
  const { slug } = use(params);

  return (
    <DetailClient
      slug={slug}
      label="Sources"
      backHref="/sources"
      load={getSource}
      entryType="source"
    />
  );
}
