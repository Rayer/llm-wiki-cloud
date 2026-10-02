const getPlatformEnv = (name) => {
  const vName = `VERCEL_${name}`;
  const nName = `NOW_${name}`;
  const v = process.env[vName];
  const n = process.env[nName];
  if (typeof v === "string") {
    if (typeof n === "string") {
      throw new import_errors.NowBuildError({
        code: "CONFLICTING_ENV_VAR_NAMES",
        message: `Both "${vName}" and "${nName}" env vars are defined. Please only define the "${vName}" env var.`,
        link: "https://vercel.link/combining-old-and-new-config"
      });
    }
    return v;
  }
  return n;
}
