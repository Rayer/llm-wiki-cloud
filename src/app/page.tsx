import { HomeClient } from "@/components/HomeClient";
import { PipelineClient } from "@/components/PipelineClient";

export default function Home() {
  return (
    <div className="space-y-10">
      <HomeClient />
      <PipelineClient />
    </div>
  );
}
