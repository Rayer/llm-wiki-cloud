function findWorkspaceRootCandidates(startDir) {
  const { root } = parse(startDir);
  const candidates = [];
  let dir = startDir;
  for (let i = 0; i < 64; i++) {
    const type = workspaceTypeOf(dir);
    if (type) {
      candidates.unshift({ dir, type });
    }
    if (dir === root)
      break;
    const parent = dirname(dir);
    if (parent === dir)
      break;
    dir = parent;
  }
  return candidates;
}
function workspaceTypeOf(dir) {
  if (existsSync(join2(dir, "pnpm-workspace.yaml"))) {
    return "pnpm";
  }
  const pkgPath = join2(dir, "package.json");
  if (existsSync(pkgPath)) {
    try {
      const pkg = JSON.parse(readFileSync2(pkgPath, "utf8"));
      const { workspaces } = pkg;
      if (Array.isArray(workspaces) && workspaces.length > 0 || workspaces && typeof workspaces === "object" && Array.isArray(workspaces.packages) && workspaces.packages.length > 0) {
        return "npm";
      }
    } catch {
    }
  }
  return null;
}
function readWorkspacePatterns(candidate) {
  try {
    if (candidate.type === "pnpm") {
      const doc = js_yaml_default.load(
        readFileSync2(join2(candidate.dir, "pnpm-workspace.yaml"), "utf8")
      );
      const packages2 = doc?.packages;
      return Array.isArray(packages2) ? packages2.filter((p) => typeof p === "string") : null;
    }
    const pkg = JSON.parse(
      readFileSync2(join2(candidate.dir, "package.json"), "utf8")
    );
    const { workspaces } = pkg;
    const packages = Array.isArray(workspaces) ? workspaces : workspaces?.packages;
    return Array.isArray(packages) ? packages.filter((p) => typeof p === "string") : null;
  } catch {
    return null;
  }
}
function workspaceClaims(candidate, memberDir) {
  const rel = normalizeRelative(relative(candidate.dir, memberDir));
  if (rel === "") {
    return false;
  }
  const patterns = readWorkspacePatterns(candidate);
  if (!patterns || patterns.length === 0) {
    return false;
  }
  const positives = [];
  const negatives = [];
  for (const pattern of patterns) {
    if (pattern.startsWith("!")) {
      negatives.push(normalizeRelative(pattern.slice(1)));
    } else {
      positives.push(normalizeRelative(pattern));
    }
  }
  const matches = (pattern) => (0, import_minimatch.default)(rel, pattern, { dot: false });
  if (!positives.some(matches) || negatives.some(matches)) {
    return false;
  }
  return existsSync(join2(memberDir, "package.json"));
}
function resolvePerDirectoryLinkRoot(anchorDir, rootDirectorySetting) {
  let repoRoot = anchorDir;
  for (const candidate of findWorkspaceRootCandidates(anchorDir)) {
    if (workspaceClaims(candidate, anchorDir)) {
      repoRoot = candidate.dir;
      break;
    }
  }
  const linkLocation = normalizeRelative(relative(repoRoot, anchorDir));
  if (linkLocation === "") {
    return { repoRoot, resolvedRootDirectory: "" };
  }
  const setting = normalizeRelative(rootDirectorySetting ?? "");
  if (setting === "") {
    return { repoRoot, resolvedRootDirectory: linkLocation };
  }
  if (existsSync(join2(anchorDir, setting))) {
    return {
      repoRoot,
      resolvedRootDirectory: normalizeRelative(
        relative(repoRoot, join2(anchorDir, setting))
      )
    };
  }
  return {
    repoRoot,
    resolvedRootDirectory: linkLocation,
    advisory: `Ignoring "rootDirectory" setting "${setting}" for the project linked in "${anchorDir}": "${join2(anchorDir, setting)}" does not exist, so the build will use the linked directory "${linkLocation}" instead. Remove the "rootDirectory" setting, or configure it at the repository root.`
  };
}
function normalizeRelative(p) {
  const normalized = p.replace(/\\/g, "/").replace(/^\.\//, "").replace(/^\/+/, "").replace(/\/+$/, "");
  return normalized === "." ? "" : normalized;
}
