"use client";

import { ListClient } from "@/components/ListClient";
import { getConcepts } from "@/lib/api";

export default function ConceptsPage() {
  return (
    <ListClient
      title="Concepts"
      description="Distilled concept pages generated from the LLM Wiki corpus."
      load={getConcepts}
      basePath="/concepts"
    />
  );
}
