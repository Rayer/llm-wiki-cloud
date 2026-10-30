import { HomeClient } from "@/components/HomeClient";
import { PipelineClient } from "@/components/PipelineClient";
import { DocumentImportFailureNotice } from "@/components/DocumentImportFailureNotice";

export default function Home() {
  return (
    <div className="space-y-10">
      <DocumentImportFailureNotice />
      <HomeClient />
      <PipelineClient />
    </div>
  );
}
