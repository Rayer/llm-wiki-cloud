# DEV 37094836912 — Frontend build workPath repair

This offline repair responds to the source finding in Supervisor review `ef06afe96a3a3237903f89d6`. It proves the pinned no-workspace workPath defect and repairs the local build input handling. It does not establish which workspace ancestry or project-link contents existed in the historical runner; that live failure cause remains **UNKNOWN**.

## Pinned source and behavior

The exact `vercel@59.11.7` tarball came from the existing local npm cache only (SHA256 `34432b6f0ddd6501ab17140dcf6c5baa2e68fa1ce91eabcb2afef4fbf4db44eb`). The source fixtures retain the workspace resolver, build caller, default output selection, and `doBuild` workPath expression. The build-caller fixture body SHA256 is `b0aa8d6efad9e8280e385d42ac8a63bdeeef25dbc3f574c4c38442c1c1c1738a`.

The source-backed two-case probe starts the CLI calls at the already validated `apps/frontend` directory and uses the actual checked-in Frontend `package.json`, without manufacturing a package at a computed workPath. Before correction, the pinned caller left `rootDirectory=apps/frontend` in place when no ancestor workspace claimed the directory, so `doBuild` resolved `apps/frontend/apps/frontend`; the claiming-ancestor control resolved to the real Frontend package. The preserved red outputs are `dev-37094836912-workpath-causal-red.txt` (SHA256 `4359a3cf2e156cddf38aef1a074bdf2029873c27a381b066052e354e17d70921`) and `dev-37094836912-workpath-causal-red-final.txt` (SHA256 `67b43beae3bf1e26443be62484945532539a4c6f56f86a1d0073da7aed471bff`); both exit 1 on the pre-fix two-case probe.

The pulled `.vercel/project.json` is now an input to the local root correction, not only diagnostic metadata. In the pinned package, `readProjectSettings()` in `dist/chunks/chunk-EPOLDWRA.js:50-63` returns `null` for malformed JSON. `dist/commands/build/index.js:4442-4460,4495-4529` treats absent settings as a request to pull them when `--yes` is present; `dist/chunks/chunk-XIFATBH5.js:162-204` shows `pullCommandLogic()` writes project settings at line 193, and the build command rereads them at line 4529 before choosing the workPath/output. Letting a malformed file pass the adapter unchanged would therefore cause Vercel to repull the configured `rootDirectory` and re-enter the doubled-root branch. The adapter must fail closed because it cannot safely remove only that field from unreadable settings.

## Local correction and retained boundaries

After the successful pull, `Providers.prepare()` reads the exact pulled link bytes and mode, parses the settings, and requires `settings.rootDirectory` to match the validated Frontend root. It removes only that local field for the build call. Pull/build cwd, argv, child context, timeouts, target, output path, strict static-config equality checks, and remote project settings are unchanged. A `finally` restores the original bytes and mode before archive creation; the canonical archive contains that restored `.vercel/project.json` and the validated output.

The test suite separates the two error boundaries:

- A malformed pulled project file is a required-input failure: `invalid-or-unreadable-input` with `JSONDecodeError`, before the build command, no Frontend receipt/archive/barrier/runtime, and unchanged Auth receipt. The malformed bytes and original file mode remain intact.
- A malformed read during post-failure diagnostic collection remains best-effort. The primary missing-output failure stays `frontend-output-missing`, while the diagnostic records `settings_root=other` and `metadata_read_failed=true`. A separate collector exception regression still verifies that an exception from the collector does not replace the primary failure.

Success and build-failure tests compare exact project-link bytes and mode after the `finally`. The success path also checks the restored link is what the canonical archive contains. Existing strict build-config validation, retained Auth artifact reuse, archive/runtime consumption, and no-ready/no-runtime behavior on prepare failure remain covered.

The earlier 13-test run is preserved at `dev-37094836912-workpath-focused-final.txt` (SHA256 `f93a136ce8db0281a3673b947f54dd382a7c64d571d7ebdf7409e2b94978c448`): **12 passed, 1 failed** because that fixture incorrectly expected a malformed required build input to remain a later `frontend-output-missing` diagnostic. It was not a passing run. The first post-edit attempt is separately preserved at `dev-37094836912-workpath-focused-remediated.txt` (SHA256 `4def09bf0534f30a905cb00305059116eb11067cd00bb3fb09b716a951846ab4`); it stopped before tests on a test-file indentation error. The earlier file named `dev-37094836912-workpath-focused-green.txt` (SHA256 `a67e3da2a6085232274cc9398925832bdb42d5fe50a0097ab9f6aab4729e2ead`) was only a two-test provider subset, not the full focused suite.

## Verification

Python 3.14.6; both commands ran from `deploy/engine/tests` with explicit execution-time scratch and bytecode settings. Test source inherits the process `TMPDIR` (or its fixture temp directory); it does not hard-code the Parent scratch path.

```sh
TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest test_prepare_diagnostics.FrontendPrepareDiagnostics -v
TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s . -p 'test_*.py' -v
```

The focused Frontend prepare class passed **14 tests** in 2.541s. Raw output: `dev-37094836912-workpath-focused-remediated-2.txt`, SHA256 `7b4bb631afe955a8084ae3f3fd8c0daa5dbbb09cace858717d33c1878a920615`.

The complete engine suite passed **79 tests** in 140.930s. Raw output: `dev-37094836912-workpath-engine-final.txt`, SHA256 `e650443d28c4b3c1616a6d1a37df71854d8e4a5dacffd086a93842fa3807b8c2`. `git diff --check` passed.

The worktree remains local and uncommitted on `Rayer/LWC-358-vercel-project-cwd`, HEAD `f1480c03fe7aa0c4a10054de741d057210310db1`, tree `e9db0197689f4e8109466474483a8a953617812c`. Frozen spec SHA256 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. No live CLI/API/pull/build, provider, npm network/install, Actions dispatch, credential/IAM, runtime, Production, Git publication, or deployment operation occurred. The canonical manifest has 51 exact bytes/modes rows, content SHA256 `32086cc748b927e4af3f2b876ddf21e4f9b7b04469c246e20e3e74e05cf988ea`, and manifest-file SHA256 `5e898e0262d1d9abe8eefb9349c31cd7aceca1c5dbbb614208893e4bf450de74`. It includes the new pinned build-caller fixture. Evidence reports and raw logs remain outside the manifest.
