# LWC-72 Admin Interface Design

## Context

`LWC-72` adds a frontend admin interface for LLM Wiki Cloud. It depends on
`LWC-108`, which restores BFF admin JWT role claims, `AdminOnly` middleware, and
the `/api/v1/admin/*` endpoints.

The frontend is a Next app with an existing dark zinc Shell, `Surface`, `Badge`,
and `apiFetch` helpers. The admin UI should match that operational interface,
not introduce a separate marketing-style page.

## Scope

Build a complete MVP at `/admin` with:

- Admin-only access based on `user.role === "admin"`.
- A single admin page with tabs for `Projects` and `Users`.
- Project listing and project actions.
- User listing and user actions.
- Inline loading, success, and error states.
- Confirmation modals for destructive or high-impact actions.

Out of scope:

- Audit logs.
- Bulk actions.
- Search/filter/pagination.
- Fine-grained roles beyond admin vs non-admin.
- Optimistic updates.

## Access Model

Update frontend auth normalization so `AuthUser` includes `role?: string`.
`normalizeAuthResponse` and `normalizeRefreshResponse` should preserve the
`role` value returned by the BFF login/refresh payload.

Add an Admin navigation item to `Shell` only when:

```ts
user?.role === "admin"
```

The `/admin` route should render a client component with these states:

- Auth hydration pending: loading state.
- Authenticated non-admin: compact 403 state inside a `Surface`.
- Admin user: admin console.

The admin route must not require a selected project. It should remain reachable
when the user has zero projects, because admin endpoints are cross-project.

## Page Structure

Create `src/app/admin/page.tsx` that renders `AdminClient`.

`AdminClient` layout:

- Header with `Admin` title, short subtitle, and a `Refresh` action.
- Inline notice area for success and error feedback.
- Segmented tabs for `Projects` and `Users`.
- One dense table per tab, wrapped in a horizontally scrollable container.

Use the existing dark zinc theme and local UI primitives:

- `Surface` for the page panels.
- `Badge` for role and status labels.
- Native semantic `<table>` markup for data grids.
- Lucide icons for row action buttons where helpful.

## Projects Tab

Fetch data from:

```http
GET /api/v1/admin/projects
```

Columns:

- Project name.
- User ID.
- Concept count.
- Source count.
- Actions.

Row actions:

- Rename: opens modal with the current name, submits
  `PATCH /api/v1/admin/projects/:id`.
- Delete: opens confirmation modal, submits
  `DELETE /api/v1/admin/projects/:id`.
- Rebuild index: opens confirmation modal, submits
  `POST /api/v1/admin/projects/:id/rebuild-index`.
- Trigger pipeline: opens confirmation modal, submits
  `POST /api/v1/admin/projects/:id/pipeline`.

After a successful project action, refresh only the projects table. Do not use
optimistic updates.

## Users Tab

Fetch data from:

```http
GET /api/v1/admin/users
```

Columns:

- Email.
- Role.
- Project count.
- Actions.

Row actions:

- Change role: opens modal with a role control. The MVP supports `admin` and
  `user`; submit `PATCH /api/v1/admin/users/:id` with `{ "role": "admin" }` or
  `{ "role": "user" }`.
- Delete user: opens confirmation modal, submits
  `DELETE /api/v1/admin/users/:id`.

After a successful user action, refresh only the users table. Do not use
optimistic updates.

## API Layer

Add admin helpers to `src/lib/api.ts`:

- `getAdminProjects()`
- `renameAdminProject(id, name)`
- `deleteAdminProject(id)`
- `rebuildAdminProjectIndex(id)`
- `triggerAdminProjectPipeline(id)`
- `getAdminUsers()`
- `updateAdminUserRole(id, role)`
- `deleteAdminUser(id)`

All admin helpers must call `apiFetch` with:

```ts
{ requireProject: false }
```

This prevents `X-Project-ID` from being sent to cross-project admin endpoints.

Normalize list payloads defensively:

- Projects can come as an array or wrapped in `projects`, `items`, `results`, or
  `data`.
- Users can come as an array or wrapped in `users`, `items`, `results`, or
  `data`.
- Counts should accept snake_case and camelCase variants, such as
  `concept_count`, `conceptCount`, `source_count`, `sourceCount`, and
  `project_count`, `projectCount`.

## Error Handling

Table load failures:

- Show a per-tab error state with retry.
- Do not clear data from the other tab.

Mutation failures:

- Keep the modal open.
- Show the error near the modal submit area or header notice.
- Re-enable the action controls.

Auth failures:

- `401` continues to use existing API auth behavior, including refresh and
  session clearing.
- `403` on admin load renders the admin access denied state.

Action pending state:

- Disable the triggering row action while the request is in flight.
- Modal submit button shows a pending label.
- Close modal only after success.

## Testing

Add or update tests covering:

- `auth.test.mjs`: role is preserved by auth and refresh normalization.
- `api.test.mjs`: admin API helpers call `/api/v1/admin/*` with
  `requireProject: false` and no `X-Project-ID`.
- Shell static test: Admin nav is gated by `user?.role === "admin"`.
- Admin static test: `AdminClient` includes Projects/Users tabs, confirmation
  modals for destructive/high-impact actions, and uses admin API helpers.

Run:

```bash
npm test
npm run lint
```

## Acceptance Criteria

- Admin users can open `/admin` and manage projects and users from a tabbed
  console.
- Non-admin users cannot access the admin console UI.
- Admin API requests do not require or send a project header.
- Rename, delete, rebuild index, trigger pipeline, change role, and delete user
  actions all have explicit confirmations or modals.
- Successful mutations refresh the relevant table.
- Errors are visible and actionable without losing page context.
