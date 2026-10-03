async function ensureLink(commandName, client, cwd, opts = {}) {
  cwd = await resolveProjectCwd(cwd);
  let link = opts.link;
  const nonInteractive = opts.nonInteractive ?? client.nonInteractive ?? false;
  opts.nonInteractive = nonInteractive;
  if (!link) {
    if (opts.forceDelete) {
      link = { status: "not_linked", org: null, project: null };
    } else {
      link = await getLinkedProject(client, {
        cwd,
        projectName: opts.projectName,
        projectNameIsExplicit: Boolean(opts.projectName && opts.failIfNotFound),
        scopeIsExplicit: detectExplicitScope(client),
        allowOwnerLookupFallback: opts.allowOwnerLookupFallback,
        skipRemoteLookup: opts.skipRemoteLookup
      });
    }
    opts.link = link;
  }
  if (link.status === "linked" && opts.forceDelete || link.status === "not_linked") {
    if (link.status === "not_linked" && opts.failIfNotFound && opts.projectName) {
      await printProjectNotFoundError(
        client,
        opts.projectName,
        commandName,
        link.orgId
      );
      return 1;
    }
    if (link.status === "not_linked" && opts.requireExistingLink) {
      output_manager_default.error(
        `Project is not linked. Run ${getCommandName("link")} first.`
      );
      return 1;
    }
    const { default: setupAndLink } = await import("./setup-and-link-KXPZE32P.js");
    link = await setupAndLink(client, cwd, opts);
    if (link.status === "not_linked") {
      return 0;
    }
  }
  if (link.status === "error") {
    if (link.reason === "HEADLESS") {
      if (nonInteractive) {
        outputActionRequired(
          client,
          {
            status: "action_required",
            reason: "confirmation_required",
            message: `Command ${getCommandNamePlain(commandName)} requires confirmation. Use option --yes to confirm.`,
            next: [
              {
                command: buildCommandWithYes(client.argv),
                when: "Confirm and run"
              }
            ]
          },
          link.exitCode
        );
      } else {
        output_manager_default.error(
          `Command ${getCommandName(
            commandName
          )} requires confirmation. Use option ${param("--yes")} to confirm.`
        );
      }
    }
    if (nonInteractive) {
      process.exit(link.exitCode);
    }
    return link.exitCode;
  }
  return link;
}

async function pullCommandLogic(client, cwd, autoConfirm, environment, flags, projectNameOrId) {
  const link = await ensureLink("pull", client, cwd, {
    autoConfirm,
    pullEnv: false,
    projectName: projectNameOrId,
    failIfNotFound: !!projectNameOrId
  });
  if (typeof link === "number") {
    return link;
  }
  const { project, org, repoRoot } = link;
  let currentDirectory;
  if (repoRoot) {
    currentDirectory = join(repoRoot, project.rootDirectory || "");
  } else {
    currentDirectory = cwd;
  }
  client.config.currentTeam = org.type === "team" ? org.id : void 0;
  const pullResultCode = await pullAllEnvFiles(
    environment,
    client,
    link,
    flags,
    currentDirectory
  );
  if (pullResultCode !== 0) {
    return pullResultCode;
  }
  output_manager_default.print("\n");
  output_manager_default.log("Downloading project settings");
  const isRepoLinked = typeof repoRoot === "string";
  await writeProjectSettings(currentDirectory, project, org, isRepoLinked);
  const settingsStamp = stamp_default();
  output_manager_default.print(
    `${prependEmoji(
      `Downloaded project settings to ${import_chalk.default.bold(
        humanizePath(join(currentDirectory, VERCEL_DIR, VERCEL_DIR_PROJECT))
      )} ${import_chalk.default.gray(settingsStamp())}`,
      emoji("success")
    )}
`
  );
  return 0;
}

async function writeProjectSettings(cwd, project, org, isRepoLinked) {
  let analyticsId;
  if (project.analytics?.id && (!project.analytics.disabledAt || project.analytics.enabledAt && project.analytics.enabledAt > project.analytics.disabledAt)) {
    analyticsId = project.analytics.id;
  }
  const projectLinkAndSettings = {
    projectId: isRepoLinked ? void 0 : project.id,
    orgId: isRepoLinked ? void 0 : org.id,
    projectName: isRepoLinked ? void 0 : project.name,
    settings: {
      createdAt: project.createdAt,
      framework: project.framework,
      devCommand: project.devCommand,
      installCommand: project.installCommand,
      buildCommand: project.buildCommand,
      outputDirectory: project.outputDirectory,
      rootDirectory: project.rootDirectory,
      directoryListing: project.directoryListing,
      nodeVersion: project.nodeVersion,
      analyticsId
    }
  };
  const path = join(cwd, VERCEL_DIR, VERCEL_DIR_PROJECT);
  return await (0, import_fs_extra.outputJSON)(path, projectLinkAndSettings, {
    spaces: 2
  });
}
