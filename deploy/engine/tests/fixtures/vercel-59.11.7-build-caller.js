// Source excerpt from the cached vercel@59.11.7 package tarball.
// dist/commands/build/index.js:4436-4445, 4534-4546, 4548-4549, 2021.
// The wrapper parameters expose the pinned caller's local variables to the offline fixture.
function pinnedBuildCallerContext(cwd, link, project, resolvePerDirectoryLinkRoot) {
  const invokedCwd = cwd;
  const hasRepoLevelLink = Boolean(link?.repoRoot);
  let projectRootDirectory = link?.projectRootDirectory ?? "";
  if (link?.repoRoot) {
    cwd = link.repoRoot;
  }
  if (!hasRepoLevelLink && link && project?.settings) {
    const resolved = resolvePerDirectoryLinkRoot(
      invokedCwd,
      project.settings.rootDirectory
    );
    if (resolved.advisory) {
      // The production caller emits this through its warning manager.
    }
    if (resolved.resolvedRootDirectory !== "") {
      projectRootDirectory = resolved.resolvedRootDirectory;
      project.settings.rootDirectory = resolved.resolvedRootDirectory;
      cwd = resolved.repoRoot;
    }
  }
  return { cwd, projectRootDirectory, project };
}

function pinnedDefaultOutputDir(cwd, projectRootDirectory, join3) {
  return join3(cwd, projectRootDirectory, ".vercel/output");
}

function pinnedDoBuildWorkPath(cwd, project, join2) {
  const workPath = join2(cwd, project.settings.rootDirectory || ".");
  return workPath;
}
