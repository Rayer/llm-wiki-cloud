#!/usr/bin/env python3
"""r2 workflow contracts, replacing the retired mixed build/mutate/CD polling graph."""
import json
from pathlib import Path
import re
import unittest

ROOT=Path(__file__).resolve().parents[1]

class EngineWorkflowContract(unittest.TestCase):
    def test_single_authority_and_one_protected_job(self):
        s=(ROOT/'.github/workflows/cd.yml').read_text()
        self.assertEqual(len(re.findall(r'^    environment:',s,re.M)),1)
        self.assertIn('group: lwc-engine-${{ inputs.environment }}',s)
        self.assertIn('cancel-in-progress: false',s)
        self.assertLess(s.index('Publish ready stage barrier'),s.index('Execute bounded runtime'))
        self.assertIn("if: inputs.operation == 'release'",s)
        runtime=s[s.index('      - name: Execute bounded runtime'):]
        self.assertNotIn('build',runtime)
        self.assertIn('operation: runtime',runtime)
        entry=(ROOT/'.github/actions/deployment-engine/index.cjs').read_text()
        self.assertIn("'--components',process.env.COMPONENTS",entry)
        for secret in ('VERCEL_TOKEN','VERCEL_PROJECT_ID','VERCEL_TEAM_ID','WIF_PROVIDER','WIF_SERVICE_ACCOUNT'):
            self.assertIn('secrets.'+secret,s)
        for forbidden in ('revalidate-ci','revalidate-before-provider','preflight-shared','provision-exportjob','gh run list','jobs execute'):
            self.assertNotIn(forbidden,s)

    def test_explicit_wrappers_and_recovery(self):
        for file,env,branch in [('deploy-dev.yml','development','develop'),('promote-production.yml','production','main')]:
            s=(ROOT/'.github/workflows'/file).read_text()
            for text in ('workflow_dispatch:','release_tag:','components:','artifact_id:','secrets: inherit','uses: ./.github/workflows/cd.yml','environment: '+env,"refs/heads/"+branch):
                self.assertIn(text,s)
            self.assertNotIn('workflow_run:',s)
        recovery=(ROOT/'.github/workflows/recover-deployment.yml').read_text()
        self.assertIn('[rollback, reactivate, deploy, tag, readback]',recovery)
        self.assertIn('uses: ./.github/workflows/cd.yml',recovery)

    def test_profiles_and_real_component_operations(self):
        profiles=json.loads((ROOT/'deploy/engine/profiles.json').read_text())
        self.assertEqual(set(profiles),{'auth','bff','worker','exportjob','frontend'})
        for c,p in profiles.items():
            self.assertGreater(p['revision'],0);self.assertTrue(p['inputs'])
        s=(ROOT/'deploy/engine/providers.py').read_text()
        for op in ('prepare','snapshot','deploy','observe','rollback','reconcile_candidate'):
            self.assertIn('def '+op+'(',s)
        self.assertIn("'--skip-domain'",s)
        self.assertIn('range(12)',s);self.assertIn('time.sleep(5)',s)
        for op in ("'create'", "'delete'", "'execute'"):
            self.assertNotIn(op,s)

if __name__=='__main__':unittest.main()
