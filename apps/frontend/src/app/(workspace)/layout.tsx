import { Shell } from '@/components/Shell';
import { RuntimeConfigBoundary } from '@/components/RuntimeConfigBoundary';
import { AuthProvider } from '@/lib/auth';

export default function WorkspaceLayout({ children }: { children: React.ReactNode }) {
  return (
    <RuntimeConfigBoundary>
      <AuthProvider>
        <Shell>{children}</Shell>
      </AuthProvider>
    </RuntimeConfigBoundary>
  );
}
