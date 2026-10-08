import { setRuntimeConfigForTests } from '../src/lib/runtime-config.ts';

setRuntimeConfigForTests({
  schema_version: 1,
  api_url: 'https://api.runtime.test',
  auth_url: 'https://auth.runtime.test',
});
