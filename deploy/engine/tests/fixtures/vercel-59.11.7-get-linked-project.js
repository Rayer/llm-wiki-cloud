async function getLinkedProject(client, options = {}) {
  let path = options.cwd ?? client.cwd;
  path = await resolveProjectCwd(path);
  const VERCEL_ORG_ID = getPlatformEnv2("ORG_ID");
  const VERCEL_PROJECT_ID = getPlatformEnv2("PROJECT_ID");
  const shouldUseEnv = Boolean(VERCEL_ORG_ID && VERCEL_PROJECT_ID);
  const projectName = options.projectName;
  const projectNameIsExplicit = options.projectNameIsExplicit === true;
  const explicitProjectName = projectNameIsExplicit ? projectName : void 0;
  const hasExplicitProject = Boolean(explicitProjectName);
  const shouldUseEnvContext = shouldUseEnv && !hasExplicitProject;
  if (!hasExplicitProject && (VERCEL_ORG_ID || VERCEL_PROJECT_ID) && !shouldUseEnv) {
    output_manager_default.error(
      `You specified ${VERCEL_ORG_ID ? "`VERCEL_ORG_ID`" : "`VERCEL_PROJECT_ID`"} but you forgot to specify ${VERCEL_ORG_ID ? "`VERCEL_PROJECT_ID`" : "`VERCEL_ORG_ID`"}. You need to specify both to deploy to a custom project.
`
    );
    return { status: "error", exitCode: 1 };
  }
  let link;
  let explicitlyResolvedProject = null;
  let orgId;
  if (explicitProjectName) {
    const hasExplicitScope = options.scopeIsExplicit === true;
    const matchingLocalLink = hasExplicitScope || shouldUseEnv ? null : await getProjectLink(client, path, explicitProjectName, true);
    const fallbackOrgId = shouldUseEnv ? VERCEL_ORG_ID : matchingLocalLink?.orgId ?? client.config.currentTeam;
    const lookupOrgId = hasExplicitScope ? client.config.currentTeam : fallbackOrgId;
    orgId = lookupOrgId;
    const apiProject = await getProjectByNameOrId(
      client,
      explicitProjectName,
      lookupOrgId
    );
    explicitlyResolvedProject = apiProject instanceof ProjectNotFound ? null : apiProject;
    if (explicitlyResolvedProject === null) {
      link = null;
    } else {
      const resolvedOrgId = explicitlyResolvedProject.accountId ?? lookupOrgId ?? matchingLocalLink?.orgId;
      let localLink = matchingLocalLink?.projectId === explicitlyResolvedProject.id ? matchingLocalLink : null;
      if (!localLink) {
        try {
          localLink = await getProjectLink(
            client,
            path,
            explicitlyResolvedProject.id,
            true
          );
        } catch (err) {
          output_manager_default.debug(
            `Ignoring local project metadata after explicit project resolution: ${err}`
          );
        }
      }
      link = resolvedOrgId ? {
        ...localLink?.projectId === explicitlyResolvedProject.id && localLink.orgId === resolvedOrgId ? localLink : {},
        projectId: explicitlyResolvedProject.id,
        orgId: resolvedOrgId
      } : null;
    }
  } else {
    link = shouldUseEnvContext ? { orgId: VERCEL_ORG_ID, projectId: VERCEL_PROJECT_ID } : await getProjectLink(client, path, projectName, projectNameIsExplicit);
    if (link) {
      orgId = link.orgId;
    }
  }
  if (!link) {
    return { status: "not_linked", org: null, project: null, orgId };
  }
  if (options.skipRemoteLookup && link.projectName) {
    const settings = link.settings;
    const localRootDirectory = link.projectRootDirectory ?? settings?.rootDirectory ?? null;
    const rootDirectory = localRootDirectory === "." ? null : localRootDirectory;
    const project2 = {
      ...settings,
      id: link.projectId,
      accountId: link.orgId,
      name: link.projectName,
      createdAt: settings?.createdAt ?? 0,
      updatedAt: settings?.updatedAt ?? settings?.createdAt ?? 0,
      rootDirectory
    };
    const localProjectLink = {
      status: "linked",
      org: {
        type: link.orgId.startsWith("team_") ? "team" : "user",
        id: link.orgId,
        slug: ""
      },
      project: project2,
      repoRoot: link.repoRoot,
      remoteLookupSkipped: true
    };
    return {
      ...localProjectLink,
      projectRootDirectory: link.projectRootDirectory,
      orgId: link.orgId
    };
  }
  output_manager_default.spinner("Retrieving project\u2026", 1e3);
  let org = null;
  let project = null;
  let ownerLookupUnavailable = false;
  try {
    const [orgResult, projectResult] = await Promise.allSettled([
      getOrgById(client, link.orgId),
      explicitlyResolvedProject ? Promise.resolve(explicitlyResolvedProject) : getProjectByNameOrId(client, link.projectId, link.orgId)
    ]);
    if (orgResult.status === "fulfilled") {
      org = orgResult.value;
    } else if (isAPIError(orgResult.reason) && (orgResult.reason.status === 404 || orgResult.reason.code === "not_found" || orgResult.reason.code === "mock_unimplemented")) {
      org = null;
    } else if (options.allowOwnerLookupFallback && isOwnerLookupUnavailableError(orgResult.reason)) {
      ownerLookupUnavailable = true;
    } else {
      throw orgResult.reason;
    }
    if (projectResult.status === "fulfilled") {
      project = projectResult.value;
    } else if (isAPIError(projectResult.reason) && (projectResult.reason.status === 404 || projectResult.reason.code === "not_found" || projectResult.reason.code === "mock_unimplemented")) {
      project = new ProjectNotFound(link.projectId);
    } else {
      throw projectResult.reason;
    }
  } catch (err) {
    if (isAPIError(err) && err.status === 403) {
      output_manager_default.stopSpinner();
      if (err.missingToken || err.invalidToken) {
        throw new InvalidToken(client.authConfig.tokenSource);
      } else if (err.code === "forbidden" || err.code === "team_unauthorized") {
        throw new NowBuildError({
          message: `Could not retrieve Project Settings. To link your Project, remove the ${code(
            VERCEL_DIR
          )} directory and deploy again.`,
          code: "PROJECT_UNAUTHORIZED",
          link: "https://vercel.link/cannot-load-project-settings"
        });
      }
    }
    throw err;
  } finally {
    output_manager_default.stopSpinner();
  }
  if (ownerLookupUnavailable) {
    if (project && !(project instanceof ProjectNotFound)) {
      const ownerId = project.accountId || link.orgId;
      const ownerLookupUnavailableLink = {
        status: "linked",
        org: {
          type: ownerId.startsWith("team_") ? "team" : "user",
          id: ownerId,
          slug: ownerId
        },
        project,
        repoRoot: link.repoRoot,
        projectRootDirectory: link.projectRootDirectory,
        orgId: link.orgId,
        ownerLookupUnavailable: true
      };
      return ownerLookupUnavailableLink;
    }
  }
  if (!org || !project || project instanceof ProjectNotFound) {
    if (shouldUseEnvContext) {
      output_manager_default.error(
        `Project not found (${JSON.stringify({
          VERCEL_PROJECT_ID,
          VERCEL_ORG_ID
        })})
`
      );
      return { status: "error", exitCode: 1, orgId };
    }
    output_manager_default.print(
      prependEmoji(
        "Your Project was either deleted, transferred to a new Team, or you don\u2019t have access to it anymore.\n",
        emoji("warning")
      )
    );
    return {
      status: "not_linked",
      org: null,
      project: null,
      orgId: link.orgId
    };
  }
  return {
    status: "linked",
    org,
    project,
    repoRoot: link.repoRoot,
    projectRootDirectory: link.projectRootDirectory,
    orgId: link.orgId
  };
}
