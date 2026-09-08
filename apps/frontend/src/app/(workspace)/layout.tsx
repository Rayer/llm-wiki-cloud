import { Shell } from '@/components/Shell';
import { AuthProvider } from '@/lib/auth';

export default function WorkspaceLayout({ children }: { children: React.ReactNode }) {
  return (
    <AuthProvider>
      <Shell>{children}</Shell>
    </AuthProvider>
  );
}
