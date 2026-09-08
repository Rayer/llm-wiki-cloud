"use client";

import { use } from "react";
import { DetailClient } from "@/components/DetailClient";
import { getConcept } from "@/lib/api";

export default function ConceptDetailPage({
  params,
}: {
  params: Promise<{ slug: string }>;
}) {
  const { slug } = use(params);

  return (
    <DetailClient
      slug={slug}
      label="Concepts"
      backHref="/concepts"
      load={getConcept}
      entryType="concept"
    />
  );
}
