This fixture is the unchanged `src/shared/search.ts` from
`actions/upload-artifact` commit
`ea165f8d65b6e75b540449e92b4886f43607fa02` (v4.6.2).

The source resolves a scalar directory input to the root used by the pinned
`@actions/artifact` SDK. The offline Release Notes boundary test executes this
function, passes its returned files and root to the real `uploadArtifact`
implementation from `@actions/artifact@2.3.2`, then exercises that SDK's
download and extraction path against an owned loopback server.

SHA-256: `4cbd489c239640073ee1bd17b9f941aabcf34cb7f41d68edd2264ee0438174e9`.
