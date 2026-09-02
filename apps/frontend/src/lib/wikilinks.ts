export type WikilinkSection = 'sources' | 'concepts';

type ResolvedWikilink = {
  href: string | null;
  label: string;
  dead?: boolean;
};

const canonicalIdSlugPattern = /^(?:[a-f0-9]{12}|[0-7][0-9A-HJKMNP-TV-Z]{25})-(.+)$/;

function canonicalSlug(path: string): string {
  return canonicalIdSlugPattern.exec(path)?.[1] ?? path;
}

export function resolveWikilink(
  raw: string,
  section: WikilinkSection,
  existingConceptSlugs?: Set<string>,
): ResolvedWikilink {
  const aliasSeparator = raw.indexOf('|');
  const target = aliasSeparator >= 0 ? raw.slice(0, aliasSeparator) : raw;
  const alias = aliasSeparator >= 0 ? raw.slice(aliasSeparator + 1) : undefined;
  const trimmedTarget = target.trim();
  const label = (alias ?? displayLabel(trimmedTarget)).trim();
  const routeSection = resolveRouteSection(trimmedTarget, section);

  if (
    routeSection === 'concepts' &&
    existingConceptSlugs &&
    !conceptSlugExists(trimmedTarget, existingConceptSlugs)
  ) {
    return {
      href: null,
      label,
      dead: true,
    };
  }

  const resolved: ResolvedWikilink = {
    href: wikilinkHref(trimmedTarget, section),
    label,
  };
  if (existingConceptSlugs) {
    resolved.dead = false;
  }
  return resolved;
}

function resolveRouteSection(target: string, section: WikilinkSection): WikilinkSection {
  const collectionTarget = /^(concepts|sources)\/(.+)$/.exec(target);
  if (collectionTarget) {
    return collectionTarget[1] as WikilinkSection;
  }
  return section;
}

function conceptSlugExists(target: string, existingConceptSlugs: Set<string>): boolean {
  const slug = conceptSlugForLookup(target);
  return existingConceptSlugs.has(slug);
}

function conceptSlugForLookup(target: string): string {
  const collectionTarget = /^(?:concepts|sources)\/(.+)$/.exec(target);
  const path = collectionTarget ? collectionTarget[1] : target;
  const [pathWithoutHash] = path.split('#', 1);
  return canonicalSlug(pathWithoutHash);
}

function wikilinkHref(target: string, section: WikilinkSection): string {
  const collectionTarget = /^(concepts|sources)\/(.+)$/.exec(target);
  if (collectionTarget) {
    const [, collection, path] = collectionTarget;
    return `/${collection}/${encodePathWithHash(path)}`;
  }
  return `/${section}/${encodePathWithHash(target)}`;
}

function displayLabel(target: string): string {
  const collectionTarget = /^(?:concepts|sources)\/(.+)$/.exec(target);
  const path = collectionTarget ? collectionTarget[1] : target;
  const [pathWithoutHash] = path.split('#', 1);
  return canonicalSlug(pathWithoutHash);
}

function encodePathWithHash(path: string): string {
  const [pathWithoutHash, hash] = path.split('#', 2);
  const encodedPath = pathWithoutHash.split('/').map(encodeURIComponent).join('/');
  if (hash === undefined) return encodedPath;
  return `${encodedPath}#${encodeURIComponent(hash)}`;
}