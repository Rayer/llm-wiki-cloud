// Extracted from vercel@59.11.7 dist/commands/deploy/index.js:1746-1774.
function selectPrebuiltLocation(cwd, link) {
  const { project } = link;
  const rootDirectory = project.rootDirectory;
  if (link.repoRoot) {
    cwd = link.repoRoot;
  }
  const prebuilt = true;
  let vercelOutputDir;
  if (prebuilt) {
    vercelOutputDir = join4(cwd, ".vercel/output");
    if (link.repoRoot && link.project.rootDirectory) {
      vercelOutputDir = join4(cwd, link.project.rootDirectory, ".vercel/output");
    }
  }
  return {
    cwd,
    rootDirectory,
    validationPath: rootDirectory ? join4(cwd, rootDirectory) : null,
    prebuiltOutput: vercelOutputDir
  };
}
