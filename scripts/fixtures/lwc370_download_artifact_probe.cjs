/* Execute the vendored pinned download-artifact source with a local artifact client. */
const fs = require('node:fs')
const path = require('node:path')

const repoRoot = path.resolve(__dirname, '..', '..')
const fixture = path.join(
  repoRoot,
  'scripts/fixtures/actions-download-artifact-d3f86a106a0bac45b974a628896c90dbdf5c8093/src/download-artifact.ts'
)
const typescript = require(path.join(repoRoot, 'apps/frontend/node_modules/typescript'))
const source = fs.readFileSync(fixture, 'utf8')
const compiled = typescript.transpileModule(source, {
  compilerOptions: {
    module: typescript.ModuleKind.CommonJS,
    target: typescript.ScriptTarget.ES2020,
    esModuleInterop: true
  }
}).outputText

const artifacts = JSON.parse(fs.readFileSync(process.env.LWC_TEST_ARTIFACTS_JSON, 'utf8'))
const bundlesDirectory = process.env.LWC_TEST_BUNDLES_DIR
const downloadsLog = process.env.LWC_TEST_DOWNLOADS_LOG
const appendLog = entry => fs.appendFileSync(downloadsLog, `${JSON.stringify(entry)}\n`)
const fail = message => {
  console.error(message)
  process.exitCode = 1
}
const inputName = key => `INPUT_${key.replace(/-/g, '_').toUpperCase()}`
const core = {
  getInput(key) {
    return process.env[inputName(key)] || ''
  },
  getBooleanInput(key) {
    const value = this.getInput(key).toLowerCase()
    if (value === 'true') return true
    if (value === 'false' || value === '') return false
    throw new Error(`Invalid boolean input ${key}`)
  },
  info() {},
  debug() {},
  warning() {},
  setOutput() {},
  setFailed: fail
}
const artifactClient = {
  async listArtifacts(options) {
    const runId = options.findBy && options.findBy.workflowRunId
    if (!runId || String(runId) !== process.env.LWC_TEST_EXPECTED_RUN_ID) {
      throw new Error('controlled client received an unexpected workflow run')
    }
    appendLog({
      op: 'list',
      latest: options.latest,
      workflowRunId: runId,
      repositoryOwner: options.findBy.repositoryOwner,
      repositoryName: options.findBy.repositoryName
    })
    return {artifacts}
  },
  async downloadArtifact(id, options) {
    appendLog({op: 'download', id, path: options.path})
    const bundle = path.join(bundlesDirectory, String(id))
    if (!fs.existsSync(bundle)) throw new Error(`controlled artifact ${id} is missing`)
    fs.mkdirSync(options.path, {recursive: true})
    for (const entry of fs.readdirSync(bundle, {withFileTypes: true})) {
      const sourcePath = path.join(bundle, entry.name)
      const targetPath = path.join(options.path, entry.name)
      if (entry.isDirectory()) fs.cpSync(sourcePath, targetPath, {recursive: true})
      else fs.copyFileSync(sourcePath, targetPath)
    }
    return {digestMismatch: false}
  }
}
const constants = {
  Inputs: {
    Name: 'name',
    Path: 'path',
    GitHubToken: 'github-token',
    Repository: 'repository',
    RunID: 'run-id',
    Pattern: 'pattern',
    MergeMultiple: 'merge-multiple',
    ArtifactIds: 'artifact-ids'
  },
  Outputs: {DownloadPath: 'download-path'}
}
const localRequire = name => {
  if (name === '@actions/core') return core
  if (name === '@actions/artifact') return artifactClient
  if (name === './constants') return constants
  if (name === 'minimatch') return {Minimatch: class Minimatch { match() { return false } }}
  return require(name)
}
const loaded = {exports: {}}
new Function('require', 'module', 'exports', compiled)(localRequire, loaded, loaded.exports)
