#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BFF_DIR="$ROOT/apps/bff"
FRONTEND_RESPONSE_PATH=""
FRONTEND_MUTATION_FAILURE=""
FRONTEND_DEPLOYMENT_RESULT=not_attempted
SERVICE_READBACK='{}'
SERVICE_READBACK_RESULT=unknown
WORKER_READBACK='{}'
WORKER_READBACK_RESULT=unknown
FRONTEND_READBACK='{}'
FRONTEND_READBACK_RESULT=unknown
VERCEL_READBACK_RESULT=unknown
ROLLBACK_COMPONENT_READBACK='{}'
ROLLBACK_COMPONENT_STATE=unknown

cleanup_frontend_response() {
  if [[ -n "$FRONTEND_RESPONSE_PATH" ]]; then
    rm -f "$FRONTEND_RESPONSE_PATH"
    FRONTEND_RESPONSE_PATH=""
  fi
}

trap cleanup_frontend_response EXIT

die() { echo "cd contract failed: $*" >&2; exit 1; }
need() { [[ -n "${!1:-}" ]] || die "$1 is required"; }
plan_json() { jq -er ".normalized$1" "$PLAN_PATH"; }
has_component() { jq -e --arg name "$1" '.normalized.selected_components | index($name) != null' "$PLAN_PATH" >/dev/null; }
join_config_list() { plan_json "$1 | join(\",\")"; }
mutation_accepted() {
  local component="$1"
  mkdir -p "$(dirname "$JOURNAL_PATH")"
  [[ -f "$JOURNAL_PATH" ]] || printf '[]\n' > "$JOURNAL_PATH"
  jq --arg component "$component" 'if index($component) == null then . + [$component] else error("duplicate mutation acceptance") end' "$JOURNAL_PATH" > "$JOURNAL_PATH.tmp"
  mv "$JOURNAL_PATH.tmp" "$JOURNAL_PATH"
}

service_expected() {
  local component="$1" image="$2" revision="$3"
  jq -n \
    --arg component "$component" --arg image "$image" --arg revision "$revision" \
    --arg source_sha "$SOURCE_SHA" --argjson normalized "$(jq '.normalized' "$PLAN_PATH")" '
    $normalized as $n |
    ($n[$component]) as $cfg |
    (if $component == "auth" then
      {GCP_PROJECT:$n.gcp.project_id, FIRESTORE_DATABASE_ID:$cfg.firestore_database_id,
       ALLOWED_ORIGINS:($cfg.allowed_origins | join(",")), ALLOWED_HOSTS:($cfg.allowed_hosts | join(",")),
       DEV_JWT:"false", LWC_SOURCE_COMMIT:$source_sha}
     else
      {GCP_PROJECT:$n.gcp.project_id, BUCKET:$cfg.bucket, FIRESTORE_DATABASE_ID:$cfg.firestore_database_id,
       PIPELINE_JOB_URL:$cfg.pipeline_job_url, ALLOWED_ORIGINS:($cfg.allowed_origins | join(",")),
       AUTH_SERVICE_URL:$cfg.auth_service_url, QUERY_STAGE_CONFIG_PATH:$n.query_config.runtime_path,
       DEV_JWT:"false", LWC_SOURCE_COMMIT:$source_sha}
     end) as $env |
    (if $component == "auth" then
      {JWT_SECRET:{secret:$cfg.secret_references.jwt,version:"latest",plaintext:false}}
     else
      {JWT_SECRET:{secret:$cfg.secret_references.jwt,version:"latest",plaintext:false},
       DEEPSEEK_API_KEY:{secret:$cfg.secret_references.deepseek_api_key,version:"latest",plaintext:false}}
     end) as $secrets |
    {component:$component,revision:$revision,image:$image,service_account:$cfg.runtime_service_account,runtime_service_account:$cfg.runtime_service_account,
     env:$env,secret_references:$secrets,
     network:{network:$cfg.network,subnet:$cfg.subnet,vpc_egress:$cfg.vpc_egress,ingress:$cfg.ingress,max_instances:$cfg.max_instances},
     traffic:[{revision_name:$revision,percent:100,tag:null}],
     component_config:(if $component == "auth" then
       {firestore_database_id:$cfg.firestore_database_id,allowed_origins:$cfg.allowed_origins,allowed_hosts:$cfg.allowed_hosts}
      else
       {bucket:$cfg.bucket,firestore_database_id:$cfg.firestore_database_id,pipeline_job_url:$cfg.pipeline_job_url,
        allowed_origins:$cfg.allowed_origins,auth_service_url:$cfg.auth_service_url,
        query_config:{runtime_path:$n.query_config.runtime_path}}
      end)}'
}

normalize_service_readback() {
  local component="$1" service_json="$2" revision_json="$3" revision="$4" image="$5"
  jq -ce \
    --arg component "$component" --arg revision "$revision" --arg image "$image" \
    --argjson service "$service_json" --argjson revision_json "$revision_json" '
    def template_spec: ($service.spec.template.spec // {});
    def revision_spec: ($revision_json.spec // {});
    def containers: (($revision_spec.containers // $revision_spec.template.spec.containers // template_spec.containers // []) | map(select(type == "object")));
    def env_entries: (containers | map(.env // []) | add // []);
    def env_value($name): ([env_entries[] | select(.name == $name)] | if length == 1 and (.[0].value | type) == "string" then .[0].value else null end);
    def secret_value($name):
      ([env_entries[] | select(.name == $name)] | if length != 1 then {secret:null,version:null,plaintext:false}
       else .[0] as $entry |
         ($entry.valueSource.secretKeyRef // $entry.valueFrom.secretKeyRef // null) as $ref |
         {secret:($ref.secret // $ref.name // null),version:($ref.version // $ref.key // null),plaintext:($ref == null and ($entry | has("value")))}
       end);
    def annotations: (($service.metadata.annotations // {}) + (template_spec.metadata.annotations // {}) + ($revision_json.metadata.annotations // {}));
    def interfaces: try (annotations["run.googleapis.com/network-interfaces"] | fromjson) catch [];
    def max_instances:
      (annotations["autoscaling.knative.dev/maxScale"] // annotations["run.googleapis.com/maxScale"] //
       $service.spec.template.scaling.maxInstanceCount // $service.spec.template.spec.maxInstanceCount // null)
      | if . == null then null else tonumber? end;
    def traffic: [($service.status.traffic // [])[] | {revision_name:.revisionName,percent:.percent,tag:(.tag // null)}];
    (revision_spec.serviceAccountName // revision_spec.template.spec.serviceAccountName // template_spec.serviceAccountName // null) as $account |
    (if $component == "auth" then
      {GCP_PROJECT:env_value("GCP_PROJECT"),FIRESTORE_DATABASE_ID:env_value("FIRESTORE_DATABASE_ID"),
       ALLOWED_ORIGINS:env_value("ALLOWED_ORIGINS"),ALLOWED_HOSTS:env_value("ALLOWED_HOSTS"),
       DEV_JWT:env_value("DEV_JWT"),LWC_SOURCE_COMMIT:env_value("LWC_SOURCE_COMMIT")}
     else
      {GCP_PROJECT:env_value("GCP_PROJECT"),BUCKET:env_value("BUCKET"),FIRESTORE_DATABASE_ID:env_value("FIRESTORE_DATABASE_ID"),
       PIPELINE_JOB_URL:env_value("PIPELINE_JOB_URL"),ALLOWED_ORIGINS:env_value("ALLOWED_ORIGINS"),
       AUTH_SERVICE_URL:env_value("AUTH_SERVICE_URL"),QUERY_STAGE_CONFIG_PATH:env_value("QUERY_STAGE_CONFIG_PATH"),
       DEV_JWT:env_value("DEV_JWT"),LWC_SOURCE_COMMIT:env_value("LWC_SOURCE_COMMIT")}
     end) as $env |
    (if $component == "auth" then {JWT_SECRET:secret_value("JWT_SECRET")} else
      {JWT_SECRET:secret_value("JWT_SECRET"),DEEPSEEK_API_KEY:secret_value("DEEPSEEK_API_KEY")}
     end) as $secrets |
    {component:$component,revision:$revision,image:(($revision_json.status.imageDigest // (containers[0].image // null))),
     service_account:$account,runtime_service_account:$account,env:$env,secret_references:$secrets,
     network:{network:(interfaces[0].network // null),subnet:(interfaces[0].subnetwork // interfaces[0].subnet // null),
       vpc_egress:(annotations["run.googleapis.com/vpc-access-egress"] // $service.spec.template.vpcAccess.egress // null),
       ingress:($service.metadata.annotations["run.googleapis.com/ingress"] // $service.spec.ingress // "all"),max_instances:max_instances},
     traffic:traffic,
     component_config:(if $component == "auth" then
       {firestore_database_id:env_value("FIRESTORE_DATABASE_ID"),allowed_origins:(env_value("ALLOWED_ORIGINS") | if . == null then null else split(",") end),allowed_hosts:(env_value("ALLOWED_HOSTS") | if . == null then null else split(",") end)}
      else
       {bucket:env_value("BUCKET"),firestore_database_id:env_value("FIRESTORE_DATABASE_ID"),pipeline_job_url:env_value("PIPELINE_JOB_URL"),
        allowed_origins:(env_value("ALLOWED_ORIGINS") | if . == null then null else split(",") end),auth_service_url:env_value("AUTH_SERVICE_URL"),
        query_config:{runtime_path:env_value("QUERY_STAGE_CONFIG_PATH")}}
      end)}'
}

normalize_worker_definition() {
  jq -ce '
    def safe_env:
      map(if (.valueSource.secretKeyRef? // .valueFrom.secretKeyRef?) then
            ((.valueSource.secretKeyRef // .valueFrom.secretKeyRef) as $ref |
             {name, valueSource:{secretKeyRef:{secret:($ref.secret // $ref.name),version:($ref.version // $ref.key // "latest")}}})
          elif ((.name // "") | test("(?i)(secret|token|password|api[_-]?key)")) and has("value") then
            error("plaintext sensitive Job environment value")
          else . end);
    del(.status,.metadata.uid,.metadata.resourceVersion,.metadata.generation,.metadata.creationTimestamp,.metadata.updateTime,.metadata.selfLink,.metadata.managedFields)
    | .spec.template.spec.template.spec.containers
      |= map(.env |= safe_env)
    | {apiVersion,kind,metadata,spec}
  '
}

worker_expected() {
  local image="$1"
  jq -n --arg image "$image" --argjson normalized "$(jq '.normalized' "$PLAN_PATH")" '
    $normalized as $n |
    {image:$image,service_account:$n.worker.runtime_service_account,
     env:{BUCKET:$n.worker.bucket,PIPELINE_JOB_NAME:$n.worker.job_name,PIPELINE_JOB_LOCATION:$n.worker.location},
     secret_references:{DEEPSEEK_API_KEY:{secret:$n.worker.secret_references.deepseek_api_key,version:"latest"}},
     args:$n.worker.args,volumes:[],volume_mounts:[]}'
}

normalize_worker_readback() {
  local definition="$1"
  jq -ce --argjson definition "$definition" '
    ($definition.spec.template.spec.template.spec) as $spec |
    ($spec.containers // []) as $containers |
    if ($containers | length) != 1 then error("Worker container shape is not exact") else $containers[0] end as $container |
    def env_entries: ($container.env // []);
    def env_value($name): ([env_entries[] | select(.name == $name)] | if length == 1 and (.[0].value | type) == "string" then .[0].value else null end);
    def secret_value($name): ([env_entries[] | select(.name == $name)] | if length != 1 then {secret:null,version:null,plaintext:false} else .[0] as $entry | ($entry.valueSource.secretKeyRef // $entry.valueFrom.secretKeyRef // null) as $ref | {secret:($ref.secret // $ref.name // null),version:($ref.version // $ref.key // null),plaintext:($ref == null and ($entry | has("value")))} end);
    {image:$container.image,service_account:($spec.serviceAccountName // null),
     env:{BUCKET:env_value("BUCKET"),PIPELINE_JOB_NAME:env_value("PIPELINE_JOB_NAME"),PIPELINE_JOB_LOCATION:env_value("PIPELINE_JOB_LOCATION")},
     secret_references:{DEEPSEEK_API_KEY:secret_value("DEEPSEEK_API_KEY")},args:($container.args // []),
     volumes:($spec.volumes // []),volume_mounts:($container.volumeMounts // [])}'
}

verify_worker_definition() {
  local observed="$1" expected="$2" comparison
  comparison=$(jq -n --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed')
  [[ "$comparison" == true ]]
}

validate_inputs() {
  need ENVIRONMENT; need SOURCE_REF; need SOURCE_SHA; need CONFIG_PATH; need COMPONENTS; need PLAN_PATH
  [[ "$ENVIRONMENT" == development || "$ENVIRONMENT" == production ]] || die "environment is not allowlisted"
  [[ "$SOURCE_REF" == develop || "$SOURCE_REF" == main ]] || die "source ref is not allowlisted"
  [[ "$ENVIRONMENT" == development && "$SOURCE_REF" == develop || "$ENVIRONMENT" == production && "$SOURCE_REF" == main ]] || die "environment and source ref do not match"
  [[ "${GITHUB_REF:-}" == "refs/heads/$SOURCE_REF" && "${GITHUB_REF_NAME:-}" == "$SOURCE_REF" ]] || die "workflow ref is not the canonical source ref"
  [[ "$SOURCE_SHA" =~ ^[0-9a-f]{40}$ ]] || die "source SHA is not an exact lowercase SHA"
  [[ "$CONFIG_PATH" == "deploy/environments/$ENVIRONMENT.yaml" ]] || die "config path is not fixed for the environment"
}

plan() {
  validate_inputs
  git fetch origin "$SOURCE_REF" --force --no-tags
  [[ "$(git rev-parse HEAD)" == "$SOURCE_SHA" ]] || die "checked-out source does not match the selected SHA"
  [[ "$(git rev-parse "origin/$SOURCE_REF")" == "$SOURCE_SHA" ]] || die "canonical source ref advanced"

  local ci_runs ci_run
  ci_runs=$(gh api "repos/${GITHUB_REPOSITORY}/actions/workflows/ci.yml/runs?head_sha=${SOURCE_SHA}&event=push&branch=${SOURCE_REF}&per_page=100")
  strict_json <<<"$ci_runs" || die "canonical CI runs response is malformed"
  ci_run=$(jq -ce --arg sha "$SOURCE_SHA" --arg ref "$SOURCE_REF" 'first(.workflow_runs[]? | select(.path == ".github/workflows/ci.yml" and .event == "push" and .head_branch == $ref and .head_sha == $sha and .status == "completed" and .conclusion == "success")) // error("successful exact canonical CI run not found")' <<<"$ci_runs")

  mkdir -p "$(dirname "$PLAN_PATH")"
  (cd "$BFF_DIR" && go run ./cmd/deploy_config --environment "$ENVIRONMENT" --config "$ROOT/$CONFIG_PATH" --components "$COMPONENTS") > "$PLAN_PATH.normalized"
  jq -n \
    --slurpfile normalized "$PLAN_PATH.normalized" \
    --arg source_sha "$SOURCE_SHA" --arg source_ref "$SOURCE_REF" \
    --arg config_path "$CONFIG_PATH" --argjson ci "$ci_run" \
    '{source:{sha:$source_sha,ref:$source_ref},config_path:$config_path,ci:{run_id:$ci.id,run_attempt:($ci.run_attempt // 1)},normalized:$normalized[0]}' \
    > "$PLAN_PATH"
  rm -f "$PLAN_PATH.normalized"
}

strict_json() {
  python3 -c '
import json
import sys

def reject_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result

try:
    json.load(sys.stdin, object_pairs_hook=reject_duplicates)
except (ValueError, UnicodeDecodeError):
    raise SystemExit(1)
'
}

revalidate_ci() {
  need PLAN_PATH; need GH_TOKEN; need GITHUB_REPOSITORY
  local CI_RUN_ID CI_RUN_ATTEMPT current attempt jobs
  CI_RUN_ID=$(jq -er '.ci.run_id | select(type == "number" and . > 0 and floor == .)' "$PLAN_PATH") || die "pinned canonical CI run ID is invalid"
  CI_RUN_ATTEMPT=$(jq -er '.ci.run_attempt | select(type == "number" and . > 0 and floor == .)' "$PLAN_PATH") || die "pinned canonical CI run attempt is invalid"
  git fetch origin "$SOURCE_REF" --force --no-tags
  [[ "$(git rev-parse HEAD)" == "$SOURCE_SHA" ]] || die "checked-out source changed before provider work"
  [[ "$(git rev-parse "origin/$SOURCE_REF")" == "$SOURCE_SHA" ]] || die "canonical source ref advanced before provider work"

  current=$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs/${CI_RUN_ID}") || die "pinned canonical CI run is unreadable"
  strict_json <<<"$current" || die "pinned canonical CI run response is malformed"
  jq -e --arg sha "$SOURCE_SHA" --arg ref "$SOURCE_REF" --argjson attempt "$CI_RUN_ATTEMPT" '
    type == "object" and .path == ".github/workflows/ci.yml" and .event == "push" and
    .head_branch == $ref and .head_sha == $sha and .run_attempt == $attempt and
    .status == "completed" and .conclusion == "success"
  ' <<<"$current" >/dev/null || die "pinned canonical CI run no longer represents a successful exact attempt"

  attempt=$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs/${CI_RUN_ID}/attempts/${CI_RUN_ATTEMPT}") || die "pinned canonical CI attempt is unreadable"
  strict_json <<<"$attempt" || die "pinned canonical CI attempt response is malformed"
  jq -e --arg sha "$SOURCE_SHA" --arg ref "$SOURCE_REF" --argjson attempt "$CI_RUN_ATTEMPT" '
    type == "object" and .path == ".github/workflows/ci.yml" and .event == "push" and
    .head_branch == $ref and .head_sha == $sha and .run_attempt == $attempt and
    .status == "completed" and .conclusion == "success"
  ' <<<"$attempt" >/dev/null || die "pinned canonical CI attempt did not conclude successfully"

  jobs=$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs/${CI_RUN_ID}/attempts/${CI_RUN_ATTEMPT}/jobs?per_page=100") || die "pinned canonical CI jobs are unreadable"
  strict_json <<<"$jobs" || die "pinned canonical CI jobs response is malformed"
  jq -e --argjson run_id "$CI_RUN_ID" --argjson attempt "$CI_RUN_ATTEMPT" '
    (.jobs | type) == "array" and length > 0 and
    all(.[]; .run_id == $run_id and .run_attempt == $attempt and .status == "completed" and .conclusion == "success")
  ' <<<"$jobs" >/dev/null || die "pinned canonical CI jobs did not conclude successfully"
}

iam_binding_is_exact() {
  local policy="$1" role="$2" member="$3"
  jq -e --arg role "$role" --arg member "$member" '
    (.bindings | type) == "array" and
    ([.bindings[]? | select(type == "object" and .role == $role and (.members | type) == "array" and (.members | index($member) != null) and (has("condition") | not))] | length == 1)
  ' <<<"$policy" >/dev/null
}

preflight_service_account() {
  local account="$1" project="$2"
  gcloud iam service-accounts describe "$account" --project "$project" --format=json --quiet >/dev/null ||
    die "runtime service account is missing or unreadable"
}

preflight_secret() {
  local secret="$1" account="$2" project="$3" policy
  gcloud secrets describe "$secret" --project "$project" --format=json --quiet >/dev/null ||
    die "configured secret is missing or unreadable"
  policy=$(gcloud secrets get-iam-policy "$secret" --project "$project" --format=json --quiet) ||
    die "configured secret IAM policy is unreadable"
  iam_binding_is_exact "$policy" roles/secretmanager.secretAccessor "serviceAccount:$account" ||
    die "configured secret is not granted to its runtime service account"
}

preflight_public_service() {
  local service="$1" project="$2" region="$3" policy
  policy=$(gcloud run services get-iam-policy "$service" --project "$project" --region "$region" --format=json --quiet) ||
    die "service IAM policy is unreadable"
  iam_binding_is_exact "$policy" roles/run.invoker allUsers ||
    die "service IAM policy is not the reviewed public invoker binding"
}

preflight_job_binding() {
  local job="$1" project="$2" region="$3" role="$4" account="$5" policy
  policy=$(gcloud run jobs get-iam-policy "$job" --project "$project" --region "$region" --format=json --quiet) ||
    die "Job IAM policy is unreadable"
  iam_binding_is_exact "$policy" "$role" "serviceAccount:$account" ||
    die "Job IAM policy is missing its reviewed runtime binding"
}

preflight() {
  need PLAN_PATH
  [[ -s "$PLAN_PATH" ]] || die "validated plan is unavailable"
  revalidate_ci
  local project region registry repo component account secret
  project=$(plan_json '.gcp.project_id')
  region=$(plan_json '.gcp.region')
  registry=$(plan_json '.gcp.artifact_registry')
  repo="${registry##*/}"
  gcloud artifacts repositories describe "$repo" --location "$region" --project "$project" --format=json --quiet >/dev/null ||
    die "Artifact Registry repository is missing or unreadable"

  while IFS= read -r component; do
    case "$component" in
      auth)
        account=$(plan_json '.auth.runtime_service_account')
        preflight_service_account "$account" "$project"
        gcloud firestore databases describe --database "$(plan_json '.auth.firestore_database_id')" --project "$project" --format=json --quiet >/dev/null ||
          die "Auth Firestore database is missing or unreadable"
        secret=$(plan_json '.auth.secret_references.jwt')
        preflight_secret "$secret" "$account" "$project"
        preflight_public_service "$(plan_json '.auth.service_name')" "$project" "$region"
        ;;
      bff)
        account=$(plan_json '.bff.runtime_service_account')
        preflight_service_account "$account" "$project"
        gcloud firestore databases describe --database "$(plan_json '.bff.firestore_database_id')" --project "$project" --format=json --quiet >/dev/null ||
          die "BFF Firestore database is missing or unreadable"
        preflight_secret "$(plan_json '.bff.secret_references.jwt')" "$account" "$project"
        preflight_secret "$(plan_json '.bff.secret_references.deepseek_api_key')" "$account" "$project"
        preflight_public_service "$(plan_json '.bff.service_name')" "$project" "$region"
        preflight_job_binding "$(plan_json '.bff.pipeline_job_name')" "$project" "$region" roles/run.jobsExecutorWithOverrides "$account"
        ;;
      worker)
        account=$(plan_json '.worker.runtime_service_account')
        preflight_service_account "$account" "$project"
        preflight_job_binding "$(plan_json '.worker.job_name')" "$project" "$(plan_json '.worker.location')" roles/run.viewer "$account"
        ;;
      frontend) ;;
      *) die "unexpected component $component" ;;
    esac
  done < <(plan_json '.selected_components[]')
}

freeze_service() {
  local component="$1" service
  service=$(plan_json ".${component}.service_name")
  local service_json traffic revision revision_json image
  service_json=$(gcloud run services describe "$service" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format=json --quiet)
  traffic=$(jq -ce 'if (.status.traffic | type) != "array" or (.status.traffic | length) != 1 or .status.traffic[0].percent != 100 or (.status.traffic[0].revisionName | type) != "string" or (.status.traffic[0].tag? != null) then error("service traffic is not one untagged 100-percent revision") else .status.traffic[0] end' <<<"$service_json")
  revision=$(jq -er '.revisionName' <<<"$traffic")
  revision_json=$(gcloud run revisions describe "$revision" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format=json --quiet)
  image=$(jq -er '.status.imageDigest | select(type == "string" and test("@sha256:"))' <<<"$revision_json")
  local readback
  readback=$(normalize_service_readback "$component" "$service_json" "$revision_json" "$revision" "$image")
  jq -n --arg revision "$revision" --arg image "$image" --argjson traffic "$traffic" --argjson readback "$readback" \
    '{revision:$revision,image:$image,traffic:$traffic,service_account:$readback.service_account,readback:$readback}'
}

freeze_worker() {
  local job
  job=$(plan_json '.worker.job_name')
  local job_json
  job_json=$(gcloud run jobs describe "$job" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.worker.location')" --format=json --quiet)
  local definition account
  definition=$(normalize_worker_definition <<<"$job_json")
  account=$(jq -er '.spec.template.spec.template.spec.serviceAccountName // empty' <<<"$definition")
  jq -n --argjson definition "$definition" --arg account "$account" --arg location "$(plan_json '.worker.location')" \
    '{definition:$definition,location:$location,service_account:$account}'
}

vercel_api_get() {
  local endpoint="$1"
  [[ "${VERCEL_PROJECT_ID:-}" =~ ^prj_[A-Za-z0-9]+$ && "${VERCEL_TEAM_ID:-}" =~ ^team_[A-Za-z0-9]+$ ]] || return 1
  curl --fail --silent --show-error --max-time 30 --connect-timeout 10 \
    -H 'Accept: application/json' \
    -H "Authorization: Bearer ${VERCEL_TOKEN:?}" \
    "${VERCEL_API_BASE_URL:-https://api.vercel.com}${endpoint}"
}

vercel_get_alias() {
  local alias="$1" encoded
  encoded=$(jq -rn --arg alias "$alias" '$alias|@uri')
  vercel_api_get "/v4/aliases/${encoded}?teamId=${VERCEL_TEAM_ID:?}"
}

vercel_get_project() {
  local project="$1" encoded
  encoded=$(jq -rn --arg project "$project" '$project|@uri')
  vercel_api_get "/v9/projects/${encoded}?teamId=${VERCEL_TEAM_ID:?}"
}

vercel_get_deployment() {
  local deployment="$1" encoded
  encoded=$(jq -rn --arg deployment "$deployment" '$deployment|@uri')
  vercel_api_get "/v13/deployments/${encoded}?teamId=${VERCEL_TEAM_ID:?}"
}

frontend_expected_aliases() {
  case "$ENVIRONMENT" in
    development) printf '["wiki.dev.rayer.idv.tw"]\n' ;;
    production) printf '["wiki.rayer.idv.tw","llm-wiki-frontend.vercel.app"]\n' ;;
    *) return 1 ;;
  esac
}

validate_frontend_alias_config() {
  jq -e --argjson expected "$(frontend_expected_aliases)" \
    '.normalized.frontend.stable_aliases == $expected' "$PLAN_PATH" >/dev/null ||
    die "frontend aliases are not the reviewed environment allowlist"
}

vercel_get_alias_inventory() {
  local cursor='' pages=0 endpoint response page next inventory='[]' seen=''
  while (( pages < 10 )); do
    endpoint="/v4/aliases?projectId=${VERCEL_PROJECT_ID:?}&teamId=${VERCEL_TEAM_ID:?}&limit=100"
    if [[ -n "$cursor" ]]; then
      endpoint+="&until=$(jq -rn --arg cursor "$cursor" '$cursor|@uri')"
    fi
    response=$(vercel_api_get "$endpoint") || return 1
    page=$(jq -ce 'if type == "array" then . elif (.aliases | type) == "array" then .aliases else error("alias inventory is not an array") end' <<<"$response") || return 1
    jq -e 'all(.[]; type == "object" and (.alias | type) == "string" and (.projectId | type) == "string" and ((.deploymentId // .deployment_id) | type) == "string" and ((.deploymentId // .deployment_id) | startswith("dpl_")))' <<<"$page" >/dev/null || return 1
    page=$(jq -c 'map({alias,project_id:.projectId,team_id:(.teamId // .accountId // .ownerId // null),deployment_id:(.deploymentId // .deployment_id)})' <<<"$page")
    inventory=$(jq -cn --argjson current "$inventory" --argjson page "$page" '$current + $page')
    next=$(jq -r 'if (has("pagination") | not) or .pagination.next == null then "" elif (.pagination.next | type) == "number" and isfinite and floor == . and . >= 0 then (.pagination.next | tostring) else "__invalid__" end' <<<"$response")
    [[ "$next" != "__invalid__" ]] || return 1
    [[ -n "$next" ]] || { jq -cn --argjson aliases "$inventory" '{aliases:$aliases}'; return 0; }
    [[ "$next" != "$cursor" && ":$seen:" != *":$next:"* ]] || return 1
    seen="${seen:+$seen:}$next"
    cursor="$next"
    pages=$((pages + 1))
  done
  return 1
}

vercel_alias_inventory_contains() {
  local inventory="$1" alias="$2" deployment="$3"
  jq -e --arg alias "$alias" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg deployment "$deployment" \
    '.aliases | map(select(.alias == $alias and .project_id == $project and (.team_id == null or .team_id == $team) and .deployment_id == $deployment)) | length == 1' <<<"$inventory" >/dev/null
}

vercel_read_alias_authority() {
  local alias="$1" response deployment inventory
  response=$(vercel_get_alias "$alias") || return 1
  deployment=$(jq -er '.deploymentId // .deployment_id | select(type == "string" and startswith("dpl_"))' <<<"$response") || return 1
  jq -e --arg alias "$alias" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" \
    'type == "object" and .alias == $alias and .projectId == $project and ((.teamId // .accountId // .ownerId // $team) == $team)' <<<"$response" >/dev/null || return 1
  inventory=$(vercel_get_alias_inventory) || return 1
  vercel_alias_inventory_contains "$inventory" "$alias" "$deployment" || return 1
  printf '%s\n' "$deployment"
}

vercel_verify_alias_target() {
  local alias="$1" deployment="$2" response inventory
  VERCEL_READBACK_RESULT=unknown
  if ! response=$(vercel_get_alias "$alias"); then return 1; fi
  if ! jq -e --arg alias "$alias" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg deployment "$deployment" \
    'type == "object" and .alias == $alias and .projectId == $project and ((.teamId // .accountId // .ownerId // $team) == $team) and ((.deploymentId // .deployment_id) == $deployment)' <<<"$response" >/dev/null; then
    VERCEL_READBACK_RESULT=failed
    return 1
  fi
  if ! inventory=$(vercel_get_alias_inventory); then return 1; fi
  if ! vercel_alias_inventory_contains "$inventory" "$alias" "$deployment"; then VERCEL_READBACK_RESULT=failed; return 1; fi
  VERCEL_READBACK_RESULT=success
}

vercel_project_authority() {
  local project_json
  project_json=$(vercel_get_project "${VERCEL_PROJECT_ID:?}") || return 1
  jq -e --arg project_id "$VERCEL_PROJECT_ID" \
    --arg team_id "$VERCEL_TEAM_ID" \
    --arg project_name "$(plan_json '.frontend.project_name')" \
    --arg repository "$(plan_json '.frontend.repository')" \
    --arg root "$(plan_json '.frontend.root_directory')" \
    'type == "object" and .id == $project_id and (.name // "") == $project_name and
     ((.team.id // .team_id // .accountId // $team_id) == $team_id) and
     ((.link.type // "github") == "github") and
     (((.link.repo // "") == $repository) or
      (((.link.org // "") + "/" + (.link.repo // "")) == $repository)) and
     ((.rootDirectory // "") == $root)' <<<"$project_json" >/dev/null
}

frontend_vercel_environment() {
  [[ "$ENVIRONMENT" == production ]] && printf 'production\n' || printf 'preview\n'
}

frontend_deploy_target() {
  [[ "$ENVIRONMENT" == production ]] && printf 'production\n' || printf 'preview\n'
}

frontend_deployment_path() {
  printf '%s/frontend-deployment.json\n' "$ARTIFACT_DIR"
}

mutation_status_path() {
  printf '%s/mutation-status.json\n' "$ARTIFACT_DIR"
}

set_mutation_status() {
  local component="$1" result="$2"
  mkdir -p "$ARTIFACT_DIR"
  jq -n --arg component "$component" --arg result "$result" '{component:$component,result:$result}' > "$(mutation_status_path)"
}

normalize_frontend_url() {
  case "$1" in
    https://*) printf '%s\n' "$1" ;;
    http://*) die "frontend deployment URL must use HTTPS" ;;
    *) printf 'https://%s\n' "$1" ;;
  esac
}

validate_frontend_url() {
  [[ "$1" =~ ^https://[A-Za-z0-9][A-Za-z0-9.-]*\.vercel\.app$ ]]
}

vercel_validate_deployment() {
  local response="$1" deployment_id="$2" deployment_url="$3"
  jq -e \
    --arg id "$deployment_id" \
    --arg url "$deployment_url" \
    --arg project "$VERCEL_PROJECT_ID" \
    --arg team "$VERCEL_TEAM_ID" \
    --arg repository "$(plan_json '.frontend.repository')" \
    --arg source_sha "$SOURCE_SHA" \
    --arg source_ref "$SOURCE_REF" \
    --arg target "$(frontend_deploy_target)" \
    'type == "object" and .id == $id and .projectId == $project and
     ((.teamId // .accountId // .ownerId // $team) == $team) and
     ((.readyState // "READY") == "READY") and ((.target // $target) == $target) and
     ((.url // $url) | if startswith("https://") then . elif startswith("http://") then . else "https://" + . end) == $url and
     ((.gitSource.sha // .meta.githubCommitSha // $source_sha) == $source_sha) and
     ((.gitSource.ref // .meta.githubCommitRef // $source_ref) == $source_ref or
      (.gitSource.ref // .meta.githubCommitRef // ("refs/heads/" + $source_ref)) == ("refs/heads/" + $source_ref)) and
     (if (.meta.githubOrg and .meta.githubRepo) then (.meta.githubOrg + "/" + .meta.githubRepo) == $repository
      elif (.gitSource.org and .gitSource.repo) then (.gitSource.org + "/" + .gitSource.repo) == $repository
      else true end)' <<<"$response" >/dev/null
}

vercel_validate_alias_mutation_response() {
  local response="$1" alias="$2"
  jq -e --arg alias "$alias" '
    type == "object" and .alias == $alias and
    (.uid | type == "string" and test("^[A-Za-z0-9_-]{1,128}$")) and
    (.created | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?Z$")) and
    (if has("oldDeploymentId") then (.oldDeploymentId | type == "string" and test("^dpl_[A-Za-z0-9]+$")) else true end)
  ' <<<"$response" >/dev/null
}

vercel_alias_apply() {
  local deployment_id="$1" alias="$2" encoded body status='' curl_status=0 response_valid=0
  FRONTEND_MUTATION_FAILURE=""
  [[ "$deployment_id" =~ ^dpl_[A-Za-z0-9]+$ ]] || { FRONTEND_MUTATION_FAILURE="frontend alias target deployment ID is invalid"; return 1; }
  mkdir -p "$ARTIFACT_DIR"
  FRONTEND_RESPONSE_PATH=$(mktemp "$ARTIFACT_DIR/vercel-alias-response.XXXXXX")
  encoded=$(jq -rn --arg id "$deployment_id" '$id|@uri')
  body=$(jq -cn --arg alias "$alias" '{alias:$alias}')
  if status=$(curl --silent --show-error --max-time 120 --connect-timeout 10 \
    -X POST -H 'Accept: application/json' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${VERCEL_TOKEN:?}" --data "$body" \
    --output "$FRONTEND_RESPONSE_PATH" --write-out '%{http_code}' \
    "${VERCEL_API_BASE_URL:-https://api.vercel.com}/v2/deployments/${encoded}/aliases?teamId=${VERCEL_TEAM_ID:?}" 2>/dev/null); then
    curl_status=0
  else
    curl_status=$?
  fi
  if [[ "$curl_status" -eq 0 && "$status" =~ ^2[0-9]{2}$ ]] &&
    vercel_validate_alias_mutation_response "$(cat "$FRONTEND_RESPONSE_PATH")" "$alias"; then
    response_valid=1
  fi

  # A timeout or non-2xx response may have been accepted. One authoritative
  # exact read-back decides acceptance; it is never followed by a blind retry.
  if vercel_verify_alias_target "$alias" "$deployment_id"; then
    if [[ "$response_valid" -eq 1 || "$curl_status" -ne 0 || ! "$status" =~ ^2[0-9]{2}$ ]]; then
      cleanup_frontend_response
      return 0
    fi
  fi
  if [[ ! "$status" =~ ^[0-9]{3}$ ]]; then status=transport; fi
  if [[ "$response_valid" -eq 0 && "$curl_status" -eq 0 && "$status" =~ ^2[0-9]{2}$ ]]; then
    status=malformed_response
  fi
  FRONTEND_MUTATION_FAILURE="frontend alias mutation failed for $alias (status=$status)"
  cleanup_frontend_response
  return 1
}

vercel_verify_frozen_frontend() {
  [[ -s "$ROLLBACK_PATH" ]] || return 1
  local alias deployment
  while IFS=$'\t' read -r alias deployment; do
    vercel_verify_alias_target "$alias" "$deployment" || return 1
  done < <(jq -r '.handles.frontend.aliases[] | [.alias,.deployment_id] | @tsv' "$ROLLBACK_PATH")
}

freeze_frontend() {
  local aliases='[]' alias deployment
  validate_frontend_alias_config
  vercel_project_authority || die "Vercel project authority does not match the reviewed target"
  while IFS= read -r alias; do
    deployment=$(vercel_read_alias_authority "$alias") || die "Vercel alias authority does not match the frozen project target"
    aliases=$(jq --arg alias "$alias" --arg deployment "$deployment" '. + [{alias:$alias,project_id:$ENV_PROJECT,team_id:$ENV_TEAM,deployment_id:$deployment}]' \
      --arg ENV_PROJECT "${VERCEL_PROJECT_ID:?}" --arg ENV_TEAM "${VERCEL_TEAM_ID:?}" <<<"$aliases")
  done < <(plan_json '.frontend.stable_aliases[]')
  jq -n --arg project_id "${VERCEL_PROJECT_ID:?}" --arg team_id "${VERCEL_TEAM_ID:?}" --argjson aliases "$aliases" \
    '{project_id:$project_id,team_id:$team_id,aliases:$aliases}'
}

freeze() {
  need PLAN_PATH; need ROLLBACK_PATH
  [[ -s "$PLAN_PATH" ]] || die "validated plan is unavailable"
  local handles='{}' component
  while IFS= read -r component; do
    case "$component" in
      auth|bff) handles=$(jq --arg component "$component" --argjson value "$(freeze_service "$component")" '. + {($component):$value}' <<<"$handles") ;;
      worker) handles=$(jq --argjson value "$(freeze_worker)" '. + {worker:$value}' <<<"$handles") ;;
      frontend) handles=$(jq --argjson value "$(freeze_frontend)" '. + {frontend:$value}' <<<"$handles") ;;
      *) die "unexpected component $component" ;;
    esac
  done < <(plan_json '.selected_components[]')
  mkdir -p "$(dirname "$ROLLBACK_PATH")"
  jq -n --arg environment "$ENVIRONMENT" --arg source_sha "$SOURCE_SHA" --argjson selected "$(plan_json '.selected_components')" --argjson handles "$handles" \
    '{schema:"lwc-306-rollback-v1",environment:$environment,source_sha:$source_sha,selected_components:$selected,handles:$handles}' > "$ROLLBACK_PATH"
}

build_cloud_image() {
  local component="$1" docker_name config image digest
  case "$component" in
    auth) docker_name=llm-wiki-auth; config=apps/bff/cloudbuild-auth.yaml ;;
    bff) docker_name=llm-wiki-bff; config=apps/bff/cloudbuild-bff.yaml ;;
    *) die "not a Cloud Run image component: $component" ;;
  esac
  image="$(plan_json '.gcp.artifact_registry')/$docker_name:$SOURCE_SHA"
  timeout --signal=TERM --kill-after=5s 600s gcloud builds submit "$ROOT/apps/bff" --project "$(plan_json '.gcp.project_id')" --config "$ROOT/$config" \
    --substitutions="_IMAGE=$image,_APP_VERSION=$(cd "$BFF_DIR" && go run ./cmd/versioncheck VERSION),_GIT_SHA=$SOURCE_SHA,_GIT_BRANCH=$SOURCE_REF,_GIT_TAG=" \
    --quiet --suppress-logs >/dev/null
  digest=$(gcloud artifacts docker images describe "$image" --project "$(plan_json '.gcp.project_id')" --format='value(image_summary.digest)' --quiet)
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "$component image digest is invalid"
  printf '%s@%s\n' "${image%:*}" "$digest"
}

build_worker_image() {
  local image digest nonce
  nonce=$(printf '%032x' "$(date -u +%s%N)")
  image="$(plan_json '.gcp.artifact_registry')/olw-pipeline:$SOURCE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  docker build --build-arg BUILD_NONCE="$nonce" --target worker -f "$ROOT/apps/bff/cmd/olw_worker/Dockerfile" -t "$image" "$ROOT/apps/bff" >/dev/null
  docker push "$image" >/dev/null
  digest=$(gcloud artifacts docker images describe "$image" --project "$(plan_json '.gcp.project_id')" --format='value(image_summary.digest)' --quiet)
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "worker image digest is invalid"
  printf '%s@%s\n' "${image%:*}" "$digest"
}

consume_dev_images() {
  local runs run id
  need GH_TOKEN
  mkdir -p "$ARTIFACT_DIR/dev-images"
  runs=$(gh api "repos/${GITHUB_REPOSITORY}/actions/workflows/deploy-dev.yml/runs?event=workflow_dispatch&status=completed&head_sha=${SOURCE_SHA}&branch=develop&per_page=100")
  run=$(jq -ce --arg sha "$SOURCE_SHA" 'first(.workflow_runs[]? | select(.path == ".github/workflows/deploy-dev.yml" and .event == "workflow_dispatch" and .head_branch == "develop" and .head_sha == $sha and .status == "completed" and .conclusion == "success")) // error("successful exact DEV workflow_dispatch run not found")' <<<"$runs")
  id=$(jq -er '.id | select(type == "number" and . > 0)' <<<"$run")
  gh run download "$id" --repo "$GITHUB_REPOSITORY" --name "cd-images-$SOURCE_SHA" --dir "$ARTIFACT_DIR/dev-images"
}

image_for() {
  local component="$1" file
  file="$ARTIFACT_DIR/images/$component-image-$SOURCE_SHA.txt"
  if [[ "$ENVIRONMENT" == production ]]; then
    file="$ARTIFACT_DIR/dev-images/$component-image-$SOURCE_SHA.txt"
  fi
  [[ -f "$file" ]] || die "immutable $component image receipt is missing"
  local image
  image=$(tr -d '[:space:]' < "$file")
  [[ "$image" =~ ^.*/(llm-wiki-auth|llm-wiki-bff|olw-pipeline)@sha256:[0-9a-f]{64}$ ]] || die "immutable $component image receipt is invalid"
  printf '%s\n' "$image"
}

service_env_args() {
  local component="$1" origins
  case "$component" in
    auth)
      origins=$(join_config_list '.auth.allowed_origins')
      printf '%s' "GCP_PROJECT=$(plan_json '.gcp.project_id')@FIRESTORE_DATABASE_ID=$(plan_json '.auth.firestore_database_id')@ALLOWED_ORIGINS=$origins@ALLOWED_HOSTS=$(join_config_list '.auth.allowed_hosts')@DEV_JWT=false@LWC_SOURCE_COMMIT=$SOURCE_SHA"
      ;;
    bff)
      origins=$(join_config_list '.bff.allowed_origins')
      printf '%s' "GCP_PROJECT=$(plan_json '.gcp.project_id')@BUCKET=$(plan_json '.bff.bucket')@FIRESTORE_DATABASE_ID=$(plan_json '.bff.firestore_database_id')@PIPELINE_JOB_URL=$(plan_json '.bff.pipeline_job_url')@ALLOWED_ORIGINS=$origins@AUTH_SERVICE_URL=$(plan_json '.bff.auth_service_url')@QUERY_STAGE_CONFIG_PATH=$(plan_json '.query_config.runtime_path')@DEV_JWT=false@LWC_SOURCE_COMMIT=$SOURCE_SHA"
      ;;
  esac
}

deploy_service() {
  local component="$1" image service account project region env_args jwt deepseek flags=()
  image=$(image_for "$component")
  service=$(plan_json ".${component}.service_name")
  account=$(plan_json ".${component}.runtime_service_account")
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region')
  env_args=$(service_env_args "$component")
  jwt=$(plan_json ".${component}.secret_references.jwt")
  local secrets="JWT_SECRET=$jwt:latest"
  if [[ "$component" == bff ]]; then
    local deepseek
    deepseek=$(plan_json '.bff.secret_references.deepseek_api_key')
    secrets="$secrets,DEEPSEEK_API_KEY=$deepseek:latest"
  fi
  flags=(--project "$project" --region "$region" --platform managed --service-account "$account" --image "$image" \
    --network "$(plan_json ".${component}.network")" --subnet "$(plan_json ".${component}.subnet")" \
    --vpc-egress "$(plan_json ".${component}.vpc_egress")" --ingress "$(plan_json ".${component}.ingress")" \
    --max "$(plan_json ".${component}.max_instances")" --update-env-vars "^@^$env_args" \
    --remove-env-vars "QUERY_EXPANSION_MODEL,QUERY_EXPANSION_REASONING,ANSWER_SYNTHESIS_MODEL,ANSWER_SYNTHESIS_REASONING,QUERY_SELECTION_LIMIT,QUERY_SELECTION_EXPLORATION_SLOTS,QUERY_SELECTION_EVIDENCE_THRESHOLD,QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT,QUERY_EXPANSION_ATTEMPTS,QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY" \
    --update-secrets "$secrets" --quiet)
  [[ "$component" == auth || "$component" == bff ]] && flags+=(--allow-unauthenticated)
  if [[ "$ENVIRONMENT" == production ]]; then
    flags+=(--no-traffic)
  fi
  local deploy_status=0 revision
  if timeout --signal=TERM --kill-after=5s 600s gcloud run deploy "$service" "${flags[@]}" >/dev/null; then
    deploy_status=0
  else
    deploy_status=$?
  fi
  revision=$(gcloud run services describe "$service" --project "$project" --region "$region" --format='value(status.latestCreatedRevisionName)' --quiet) || die "$component created revision is unavailable after provider command status $deploy_status"
  [[ "$revision" =~ ^[a-z][a-z0-9-]{0,61}[a-z0-9]$ ]] || die "$component created revision is invalid"
  if [[ "$deploy_status" -ne 0 && "$ENVIRONMENT" == production ]]; then
    verify_service "$component" "$image" "$revision" no_traffic || die "$component provider command failed and no exact immutable definition was read back"
  elif [[ "$deploy_status" -ne 0 ]]; then
    verify_service "$component" "$image" "$revision" required || die "$component provider command failed and no exact immutable serving revision was read back"
  fi
  mutation_accepted "$component"
  if [[ "$ENVIRONMENT" == production ]]; then
    if ! timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-revisions "$revision=100" --project "$project" --region "$region" --quiet >/dev/null; then
      verify_service "$component" "$image" "$revision" required || die "$component traffic mutation failed and authoritative read-back did not converge"
    fi
  fi
  verify_service "$component" "$image" "$revision"
}

verify_service() {
  local component="$1" image="$2" revision="$3" traffic_mode="${4:-required}" service json revision_json observed expected
  SERVICE_READBACK='{}'
  SERVICE_READBACK_RESULT=unknown
  service=$(plan_json ".${component}.service_name")
  if ! json=$(gcloud run services describe "$service" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format=json --quiet); then
    echo "$component service read-back is unavailable" >&2
    return 1
  fi
  if [[ "$traffic_mode" == required ]] && ! jq -e --arg revision "$revision" '
    (.status.traffic | type) == "array" and (.status.traffic | length) == 1 and
    .status.traffic[0].revisionName == $revision and .status.traffic[0].percent == 100 and (.status.traffic[0].tag? == null)
  ' <<<"$json" >/dev/null; then
    SERVICE_READBACK_RESULT=failed
    echo "$component traffic read-back does not match the selected untagged 100-percent revision" >&2
    return 1
  fi
  if ! revision_json=$(gcloud run revisions describe "$revision" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format=json --quiet); then
    echo "$component revision read-back is unavailable" >&2
    return 1
  fi
  if ! jq -e --arg image "$image" '
    (.status.imageDigest // "") == $image and (.spec.containers[0].image // "") == $image and
    any(.status.conditions[]?; .type == "Ready" and .status == "True")
  ' <<<"$revision_json" >/dev/null; then
    SERVICE_READBACK_RESULT=failed
    echo "$component immutable image/readiness read-back does not match" >&2
    return 1
  fi
  if ! observed=$(normalize_service_readback "$component" "$json" "$revision_json" "$revision" "$image"); then
    SERVICE_READBACK_RESULT=failed
    echo "$component runtime read-back shape is unsupported" >&2
    return 1
  fi
  expected=$(service_expected "$component" "$image" "$revision")
  if ! jq -e --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed' >/dev/null; then
    SERVICE_READBACK_RESULT=failed
    echo "$component runtime read-back does not match the normalized desired definition" >&2
    return 1
  fi
  SERVICE_READBACK="$observed"
  SERVICE_READBACK_RESULT=success
}

update_worker() {
  local image job bucket args secret account
  image=$(image_for worker); job=$(plan_json '.worker.job_name'); bucket=$(plan_json '.worker.bucket'); args=$(plan_json '.worker.args | join("@")'); secret=$(plan_json '.worker.secret_references.deepseek_api_key'); account=$(plan_json '.worker.runtime_service_account')
  local update_status=0
  if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs update "$job" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.worker.location')" --service-account "$account" --image "$image" --update-secrets "DEEPSEEK_API_KEY=$secret:latest" --update-env-vars "BUCKET=$bucket,PIPELINE_JOB_NAME=$job,PIPELINE_JOB_LOCATION=$(plan_json '.worker.location')" --remove-env-vars "DATA_DIR,WORKSPACE,VAULT_PATH,WORKSPACE_DIR" --args "^@^$args" --clear-volume-mounts --clear-volumes --quiet >/dev/null; then
    update_status=0
  else
    update_status=$?
    verify_worker "$image" "$bucket" || die "Worker provider command failed and exact desired definition was not read back (status=$update_status)"
  fi
  mutation_accepted worker
  verify_worker "$image" "$bucket"
}

verify_worker() {
  local image="$1" bucket="$2" job json definition observed expected
  WORKER_READBACK='{}'
  WORKER_READBACK_RESULT=unknown
  job=$(plan_json '.worker.job_name')
  if ! json=$(gcloud run jobs describe "$job" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.worker.location')" --format=json --quiet); then
    echo "worker definition read-back is unavailable" >&2
    return 1
  fi
  if ! definition=$(normalize_worker_definition <<<"$json"); then
    WORKER_READBACK_RESULT=failed
    echo "worker definition read-back shape is unsupported" >&2
    return 1
  fi
  if ! observed=$(normalize_worker_readback "$definition"); then
    WORKER_READBACK_RESULT=failed
    echo "worker definition read-back is not normalized" >&2
    return 1
  fi
  expected=$(worker_expected "$image")
  if ! verify_worker_definition "$observed" "$expected"; then
    WORKER_READBACK_RESULT=failed
    echo "worker definition read-back does not match the normalized desired definition" >&2
    return 1
  fi
  WORKER_READBACK="$observed"
  WORKER_READBACK_RESULT=success
}

reconcile_frontend() {
  local receipt deployment_id deployment_url observed aliases='[]' alias converged
  FRONTEND_READBACK='{}'
  FRONTEND_READBACK_RESULT=unknown
  receipt=$(frontend_deployment_path)
  if [[ ! -s "$receipt" ]]; then FRONTEND_READBACK_RESULT=failed; return 1; fi
  if ! deployment_id=$(jq -er '.deployment_id | select(type == "string" and startswith("dpl_"))' "$receipt"); then FRONTEND_READBACK_RESULT=failed; return 1; fi
  if ! deployment_url=$(jq -er '.deployment_url | select(type == "string" and startswith("https://"))' "$receipt"); then FRONTEND_READBACK_RESULT=failed; return 1; fi
  if ! validate_frontend_url "$deployment_url"; then FRONTEND_READBACK_RESULT=failed; return 1; fi
  if ! observed=$(vercel_get_deployment "$deployment_id"); then return 1; fi
  if ! vercel_validate_deployment "$observed" "$deployment_id" "$deployment_url"; then FRONTEND_READBACK_RESULT=failed; return 1; fi
  while IFS= read -r alias; do
    converged=false
    if vercel_verify_alias_target "$alias" "$deployment_id"; then converged=true; else FRONTEND_READBACK_RESULT="$VERCEL_READBACK_RESULT"; return 1; fi
    aliases=$(jq --arg alias "$alias" --argjson converged "$converged" ". + [{alias:\$alias,converged:\$converged}]" <<<"$aliases")
  done < <(plan_json '.frontend.stable_aliases[]')
  FRONTEND_READBACK=$(jq -n --arg deployment_id "$deployment_id" --arg deployment_url "$deployment_url" --argjson aliases "$aliases" \
    '{deployment_id:$deployment_id,deployment_url:$deployment_url,aliases:$aliases}')
  FRONTEND_READBACK_RESULT=success
}

promote_frontend() {
  local api_url auth_url project team vercel_environment target deployment_json deployment_id deployment_url alias
  FRONTEND_DEPLOYMENT_RESULT=unknown
  set_mutation_status frontend unknown
  project=$(plan_json '.frontend.project_name'); team=$(plan_json '.frontend.team_slug')
  api_url=$(plan_json '.frontend.api_url'); auth_url=$(plan_json '.frontend.auth_url')
  validate_frontend_alias_config
  vercel_project_authority || die "Vercel project authority changed before frontend build"
  vercel_environment=$(frontend_vercel_environment); target=$(frontend_deploy_target)

  # vercel pull/build/deploy creates the one environment-specific prebuilt
  # artifact. Explicit metadata binds the local prebuilt deployment to SHA.
  (cd "$ROOT/apps/frontend" &&
    npm ci --ignore-scripts >/dev/null &&
    NEXT_PUBLIC_API_URL="$api_url" NEXT_PUBLIC_AUTH_URL="$auth_url" \
      timeout --signal=TERM --kill-after=5s 120s vercel pull "$project" --yes --environment="$vercel_environment" --scope "$team" --token "${VERCEL_TOKEN:?}" >/dev/null &&
    NEXT_PUBLIC_API_URL="$api_url" NEXT_PUBLIC_AUTH_URL="$auth_url" \
      timeout --signal=TERM --kill-after=5s 300s vercel build --scope "$team" --token "${VERCEL_TOKEN:?}" $([[ "$target" == production ]] && printf '%s' '--prod') >/dev/null)

  vercel_project_authority || die "Vercel project authority changed before frontend deployment"
  vercel_verify_frozen_frontend || die "frontend alias authority changed from the frozen rollback snapshot"
  if ! deployment_json=$(cd "$ROOT/apps/frontend" && timeout --signal=TERM --kill-after=5s 300s vercel deploy --prebuilt --yes --json --scope "$team" --token "${VERCEL_TOKEN:?}" \
    --meta "githubCommitSha=$SOURCE_SHA" --meta "githubCommitRef=$SOURCE_REF" \
    --meta "githubOrg=Rayer" --meta "githubRepo=llm-wiki-cloud" \
    $([[ "$target" == production ]] && printf '%s' '--prod')); then
    FRONTEND_DEPLOYMENT_RESULT=unknown
    die "frontend deployment result is unknown after the provider command"
  fi
  mutation_accepted frontend
  FRONTEND_DEPLOYMENT_RESULT=success
  set_mutation_status frontend success
  deployment_id=$(jq -er '.id // .deploymentId | select(type == "string" and startswith("dpl_"))' <<<"$deployment_json") || die "frontend deployment ID is not immutable"
  deployment_url=$(jq -er '.url // .deployment_url | select(type == "string")' <<<"$deployment_json") || die "frontend deployment URL is missing"
  deployment_url=$(normalize_frontend_url "$deployment_url")
  validate_frontend_url "$deployment_url" || die "frontend deployment URL is not an immutable Vercel URL"
  vercel_validate_deployment "$(vercel_get_deployment "$deployment_id")" "$deployment_id" "$deployment_url" || die "frontend deployment read-back did not match exact source and authority"
  mkdir -p "$ARTIFACT_DIR"
  jq -n --arg deployment_id "$deployment_id" --arg deployment_url "$deployment_url" --arg source_sha "$SOURCE_SHA" --arg target "$target" \
    '{deployment_id:$deployment_id,deployment_url:$deployment_url,source_sha:$source_sha,target:$target}' > "$(frontend_deployment_path)"
  while IFS= read -r alias; do
    vercel_alias_apply "$deployment_id" "$alias" || die "${FRONTEND_MUTATION_FAILURE:-frontend alias mutation failed}"
  done < <(plan_json '.frontend.stable_aliases[]')
}

mutate() {
  need PLAN_PATH; need JOURNAL_PATH; need ARTIFACT_DIR
  [[ -s "$PLAN_PATH" ]] || die "validated plan is unavailable"
  mkdir -p "$ARTIFACT_DIR/images"
  set_mutation_status selected not_started
  if [[ "$ENVIRONMENT" == production ]] &&
    { has_component auth || has_component bff || has_component worker; }; then
    consume_dev_images
  fi
  local component image
  while IFS= read -r component; do
    case "$component" in
      auth|bff)
        if [[ "$ENVIRONMENT" == development ]]; then
          image=$(build_cloud_image "$component")
          printf '%s\n' "$image" > "$ARTIFACT_DIR/images/$component-image-$SOURCE_SHA.txt"
        fi
        deploy_service "$component" ;;
      worker)
        if [[ "$ENVIRONMENT" == development ]]; then
          image=$(build_worker_image)
          printf '%s\n' "$image" > "$ARTIFACT_DIR/images/worker-image-$SOURCE_SHA.txt"
        fi
        update_worker ;;
      frontend) promote_frontend ;;
      *) die "unexpected component $component" ;;
    esac
  done < <(plan_json '.selected_components[]')
}

reconcile() {
  need PLAN_PATH; need EVIDENCE_PATH; need JOURNAL_PATH
  local failed=0 unknown=0 component image revision results='{}' mutation_components='[]' mutation_count provider_readback result
  [[ -f "$JOURNAL_PATH" ]] || printf '[]\n' > "$JOURNAL_PATH"
  mutation_components=$(cat "$JOURNAL_PATH")
  mutation_count=$(jq -er 'length' <<<"$mutation_components")
  FRONTEND_READBACK='{}'
  while IFS= read -r component; do
    case "$component" in
      auth|bff)
        if ! image=$(image_for "$component"); then
          failed=1
          results=$(jq --arg component "$component" '. + {($component):{result:"failed",reason:"immutable_image_receipt_missing"}}' <<<"$results")
          continue
        fi
        if ! revision=$(gcloud run services describe "$(plan_json ".${component}.service_name")" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format='value(status.traffic[0].revisionName)' --quiet); then
          unknown=1
          results=$(jq --arg component "$component" '. + {($component):{result:"unknown",reason:"service_revision_readback_unavailable"}}' <<<"$results")
          continue
        fi
        if verify_service "$component" "$image" "$revision"; then
          results=$(jq --arg component "$component" --argjson observed "$SERVICE_READBACK" '. + {($component):{result:"success",observed:$observed}}' <<<"$results")
        else
          if [[ "$SERVICE_READBACK_RESULT" == unknown ]]; then unknown=1; else failed=1; fi
          results=$(jq --arg component "$component" --arg state "$SERVICE_READBACK_RESULT" --argjson observed "$SERVICE_READBACK" '. + {($component):{result:$state,observed:$observed}}' <<<"$results")
        fi ;;
      worker)
        if ! image=$(image_for worker); then
          failed=1
          results=$(jq --arg component "$component" '. + {($component):{result:"failed",reason:"immutable_image_receipt_missing"}}' <<<"$results")
          continue
        fi
        if verify_worker "$image" "$(plan_json '.worker.bucket')"; then
          results=$(jq --arg component "$component" --argjson observed "$WORKER_READBACK" '. + {($component):{result:"success",observed:$observed}}' <<<"$results")
        else
          if [[ "$WORKER_READBACK_RESULT" == unknown ]]; then unknown=1; else failed=1; fi
          results=$(jq --arg component "$component" --arg state "$WORKER_READBACK_RESULT" --argjson observed "$WORKER_READBACK" '. + {($component):{result:$state,observed:$observed}}' <<<"$results")
        fi ;;
      frontend)
        if reconcile_frontend; then
          results=$(jq --arg component "$component" --argjson observed "$FRONTEND_READBACK" '. + {($component):{result:"success",observed:$observed}}' <<<"$results")
        else
          if [[ "$FRONTEND_READBACK_RESULT" == unknown ]]; then unknown=1; else failed=1; fi
          results=$(jq --arg component "$component" --arg state "$FRONTEND_READBACK_RESULT" --argjson observed "$FRONTEND_READBACK" '. + {($component):{result:$state,observed:$observed}}' <<<"$results")
        fi ;;
    esac
  done < <(plan_json '.selected_components[]')
  mkdir -p "$(dirname "$EVIDENCE_PATH")"
  if [[ "$unknown" -gt 0 ]]; then result=unknown
  elif [[ "$failed" -gt 0 && "$mutation_count" -gt 0 ]]; then result=partial
  elif [[ "$failed" -gt 0 ]]; then result=failed
  else result=success
  fi
  provider_readback=false
  [[ "$result" == success ]] && provider_readback=true
  jq -n --arg result "$result" --argjson provider_readback "$provider_readback" --argjson selected "$(plan_json '.selected_components')" --argjson components "$mutation_components" --argjson count "$mutation_count" --argjson results "$results" --argjson frontend "$FRONTEND_READBACK" \
    '{result:$result,selected_components:$selected,mutation_count:$count,mutation_components:$components,mutations:{count:$count,components:$components},provider_readback:$provider_readback,readback_components:$results,frontend:$frontend}' > "$EVIDENCE_PATH"
  return $((failed || unknown))
}

rollback_frontend() {
  local failed=0 unknown=0 alias deployment converged rows='[]'
  ROLLBACK_COMPONENT_STATE=unknown
  ROLLBACK_COMPONENT_READBACK='{}'
  while IFS=$'\t' read -r alias deployment; do
    if ! vercel_alias_apply "$deployment" "$alias"; then :; fi
    converged=false
    if vercel_verify_alias_target "$alias" "$deployment"; then
      converged=true
    elif [[ "${VERCEL_READBACK_RESULT:-unknown}" == unknown ]]; then
      unknown=1
    else
      failed=1
    fi
    if ! rows=$(jq --arg alias "$alias" --arg deployment "$deployment" --argjson converged "$converged" \
      '. + [{alias:$alias,deployment_id:$deployment,converged:$converged}]' <<<"$rows"); then
      echo "frontend rollback component evidence could not be normalized" >&2
      return 1
    fi
  done < <(jq -r '.handles.frontend.aliases[] | [.alias,.deployment_id] | @tsv' "$ROLLBACK_PATH")
  FRONTEND_ROLLBACK_READBACK=$(jq -n --argjson aliases "$rows" '{aliases:$aliases}')
  if [[ "$failed" -gt 0 ]]; then ROLLBACK_COMPONENT_STATE=failed
  elif [[ "$unknown" -gt 0 ]]; then ROLLBACK_COMPONENT_STATE=unknown
  else ROLLBACK_COMPONENT_STATE=success
  fi
  [[ "$failed" -eq 0 && "$unknown" -eq 0 ]]
}

rollback_service() {
  local component="$1" old service service_json revision_json image observed expected
  ROLLBACK_COMPONENT_STATE=unknown
  ROLLBACK_COMPONENT_READBACK='{}'
  if ! old=$(jq -er ".handles.${component}.revision" "$ROLLBACK_PATH") || ! image=$(jq -er ".handles.${component}.image" "$ROLLBACK_PATH") || ! expected=$(jq -cer ".handles.${component}.readback" "$ROLLBACK_PATH"); then
    ROLLBACK_COMPONENT_STATE=failed
    return 1
  fi
  service=$(plan_json ".${component}.service_name")
  if ! service_json=$(gcloud run services describe "$service" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format=json --quiet); then return 2; fi
  if ! jq -e --arg revision "$old" '(.status.traffic | type) == "array" and (.status.traffic | length) == 1 and .status.traffic[0].revisionName == $revision and .status.traffic[0].percent == 100 and (.status.traffic[0].tag? == null)' <<<"$service_json" >/dev/null; then
    ROLLBACK_COMPONENT_STATE=failed
    return 1
  fi
  if ! revision_json=$(gcloud run revisions describe "$old" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format=json --quiet); then return 2; fi
  if ! observed=$(normalize_service_readback "$component" "$service_json" "$revision_json" "$old" "$image"); then
    ROLLBACK_COMPONENT_STATE=failed
    return 1
  fi
  ROLLBACK_COMPONENT_READBACK="$observed"
  if ! jq -e --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed' >/dev/null; then
    ROLLBACK_COMPONENT_STATE=failed
    return 1
  fi
  ROLLBACK_COMPONENT_STATE=success
}

rollback_worker() {
  local definition path job_json observed expected
  ROLLBACK_COMPONENT_STATE=unknown
  ROLLBACK_COMPONENT_READBACK='{}'
  if ! definition=$(jq -cer '.handles.worker.definition' "$ROLLBACK_PATH"); then
    ROLLBACK_COMPONENT_STATE=failed
    return 1
  fi
  mkdir -p "$ARTIFACT_DIR"
  path=$(mktemp "$ARTIFACT_DIR/worker-rollback-definition.XXXXXX")
  printf '%s\n' "$definition" > "$path"
  timeout --signal=TERM --kill-after=5s 600s gcloud run jobs replace "$path" --project "$(plan_json '.gcp.project_id')" --region "$(jq -er '.handles.worker.location' "$ROLLBACK_PATH")" --quiet >/dev/null || :
  if ! job_json=$(gcloud run jobs describe "$(plan_json '.worker.job_name')" --project "$(plan_json '.gcp.project_id')" --region "$(jq -er '.handles.worker.location' "$ROLLBACK_PATH")" --format=json --quiet); then return 2; fi
  if ! observed=$(normalize_worker_definition <<<"$job_json"); then
    ROLLBACK_COMPONENT_STATE=failed
    return 1
  fi
  ROLLBACK_COMPONENT_READBACK="$observed"
  expected="$definition"
  if ! verify_worker_definition "$observed" "$expected"; then
    ROLLBACK_COMPONENT_STATE=failed
    return 1
  fi
  ROLLBACK_COMPONENT_STATE=success
}

rollback() {
  need PLAN_PATH; need ROLLBACK_PATH; need JOURNAL_PATH; need ROLLBACK_RESULT_PATH
  if [[ ! -s "$ROLLBACK_PATH" || ! -s "$JOURNAL_PATH" ]]; then
    jq -n '{attempted:false,result:"not_needed",rollback_verified:false,mutated_components:[],components:[]}' > "$ROLLBACK_RESULT_PATH"
    return 0
  fi
  local failed=0 unknown=0 component component_status rows='[]' mutated_components
  mutated_components=$(cat "$JOURNAL_PATH")
  FRONTEND_ROLLBACK_READBACK='{}'
  while IFS= read -r component; do
    case "$component" in
      auth|bff)
        if ! timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$(plan_json ".${component}.service_name")" --to-revisions "$(jq -er ".handles.${component}.revision" "$ROLLBACK_PATH")=100" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --quiet >/dev/null; then :; fi
        if rollback_service "$component"; then component_status=success; else component_status="$ROLLBACK_COMPONENT_STATE"; fi ;;
      worker)
        if rollback_worker; then component_status=success; else component_status="$ROLLBACK_COMPONENT_STATE"; fi ;;
      frontend)
        if rollback_frontend; then component_status=success; else component_status="$ROLLBACK_COMPONENT_STATE"; fi ;;
      *) component_status=failed ;;
    esac
    if [[ "$component_status" == failed ]]; then failed=1; elif [[ "$component_status" == unknown ]]; then unknown=1; fi
    if ! rows=$(jq --arg component "$component" --arg result "$component_status" --argjson readback "$ROLLBACK_COMPONENT_READBACK" \
      '. + [{component:$component,result:$result,verified:($result == "success"),readback:$readback}]' <<<"$rows"); then
      echo "rollback component evidence could not be normalized" >&2
      return 1
    fi
  done < <(jq -r 'reverse[]' "$JOURNAL_PATH")
  local result verified
  if [[ "$failed" -gt 0 ]]; then result=failed; verified=false
  elif [[ "$unknown" -gt 0 ]]; then result=unknown; verified=false
  else result=success; verified=true
  fi
  jq -e . <<<"$mutated_components" >/dev/null || { echo "rollback journal is not JSON" >&2; return 1; }
  jq -e . <<<"$rows" >/dev/null || { echo "rollback component evidence is not JSON" >&2; return 1; }
  jq -e . <<<"$FRONTEND_ROLLBACK_READBACK" >/dev/null || { echo "frontend rollback evidence is not JSON" >&2; return 1; }
  jq -e . <<<"$ROLLBACK_COMPONENT_READBACK" >/dev/null || { echo "component rollback evidence is not JSON" >&2; return 1; }
  jq -n --arg result "$result" --argjson verified "$verified" --argjson components "$mutated_components" --argjson rows "$rows" --argjson frontend "$FRONTEND_ROLLBACK_READBACK" \
    '{attempted:true,result:$result,mutated_components:$components,components:$rows,rollback_verified:$verified,frontend:$frontend}' > "$ROLLBACK_RESULT_PATH"
  [[ "$result" == success ]]
}

redact_evidence() {
  jq '
    walk(if type == "object" and (has("credential") or has("token") or has("password") or has("secret_value"))
         then with_entries(if (.key == "credential" or .key == "token" or .key == "password" or .key == "secret_value") then .value = "<redacted>" else . end)
         else . end)
  '
}

evidence() {
  need PLAN_PATH; need EVIDENCE_PATH; need FINAL_EVIDENCE_PATH
  local readback='{}' rollback='{}' status='{}' result next_action mutation_count mutation_components rollback_attempted rollback_result rollback_verified provider_readback partial unknown normalized
  if [[ -s "$EVIDENCE_PATH" ]] && readback=$(jq -ce . "$EVIDENCE_PATH"); then :; else readback='{"result":"unknown","mutation_count":0,"mutation_components":[]}' ; fi
  if [[ -n "${ROLLBACK_RESULT_PATH:-}" && -s "$ROLLBACK_RESULT_PATH" ]] && rollback=$(jq -ce . "$ROLLBACK_RESULT_PATH"); then :; else rollback='{"attempted":false,"result":"not_needed","rollback_verified":false,"mutated_components":[]}' ; fi
  if [[ -s "$(mutation_status_path)" ]] && status=$(jq -ce . "$(mutation_status_path)"); then :; else status='{}'; fi
  readback=$(redact_evidence <<<"$readback")
  rollback=$(redact_evidence <<<"$rollback")
  mutation_count=$(jq -er '.mutation_count // (.mutation_components | length) // 0' <<<"$readback")
  mutation_components=$(jq -c '.mutation_components // []' <<<"$readback")
  rollback_attempted=$(jq -r '.attempted // false' <<<"$rollback")
  rollback_result=$(jq -r '.result // "not_needed"' <<<"$rollback")
  rollback_verified=$(jq -r '.rollback_verified // false' <<<"$rollback")
  result=$(jq -r '.result // "unknown"' <<<"$readback")
  if [[ "$(jq -r '.result // "none"' <<<"$status")" == unknown ]]; then result=unknown; fi
  if [[ "$ENVIRONMENT" == production && "$rollback_attempted" == true ]]; then
    if [[ "$rollback_result" == success && "$rollback_verified" == true ]]; then result=rolled_back
    elif [[ "$rollback_result" == failed ]]; then result=rollback_failed
    else result=rollback_unknown
    fi
  fi
  partial=false; unknown=false; provider_readback=false
  [[ "$result" == partial ]] && partial=true
  [[ "$result" == unknown || "$result" == rollback_unknown ]] && unknown=true
  [[ "$result" == success ]] && provider_readback=true
  if [[ "$result" == success || "$result" == rolled_back ]]; then
    next_action=none
  elif [[ "$result" == rollback_failed ]]; then
    next_action="production rollback failed; investigate the frozen state manually"
  elif [[ "$result" == rollback_unknown ]]; then
    next_action="production rollback read-back is unknown; investigate manually before any retry"
  elif [[ "$ENVIRONMENT" == development ]]; then
    next_action="inspect authoritative read-back and continue manually; no automatic provider retry"
  elif [[ "$mutation_count" -eq 0 ]]; then
    next_action="fix validation or build failure; no provider mutation was accepted"
  else
    next_action="inspect the partial provider state and rollback evidence before retry"
  fi
  normalized=$(jq '.normalized | del(.auth.secret_references,.bff.secret_references,.worker.secret_references)' "$PLAN_PATH")
  jq -n --arg environment "$ENVIRONMENT" --arg source_sha "$SOURCE_SHA" --arg source_ref "$SOURCE_REF" --arg result "$result" --arg next_action "$next_action" \
    --argjson normalized "$normalized" --argjson readback "$readback" --argjson rollback "$rollback" \
    --argjson mutation_count "$mutation_count" --argjson mutation_components "$mutation_components" \
    --argjson rollback_attempted "$rollback_attempted" --argjson rollback_verified "$rollback_verified" \
    --argjson partial "$partial" --argjson unknown "$unknown" --argjson provider_readback "$provider_readback" \
    '{environment:$environment,source:{sha:$source_sha,ref:$source_ref},selected_components:$normalized.selected_components,config_fingerprint:$normalized.evidence.config_fingerprint,result:$result,mutation_count:$mutation_count,mutation_components:$mutation_components,rollback_attempted:$rollback_attempted,rollback_result:$rollback.result,rollback_verified:$rollback_verified,partial:$partial,unknown:$unknown,provider_readback:$provider_readback,readback:$readback,rollback:$rollback,next_action:$next_action}' > "$FINAL_EVIDENCE_PATH"
}

case "${1:-}" in
  plan) plan ;;
  preflight) preflight ;;
  freeze) freeze ;;
  mutate) mutate ;;
  reconcile) reconcile ;;
  rollback) rollback ;;
  evidence) evidence ;;
  *) die "usage: plan|freeze|mutate|reconcile|rollback|evidence" ;;
esac
