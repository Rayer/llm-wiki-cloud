REGION ?= asia-east1
IMAGE_REPO ?= asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-bff
IMAGE_TAG ?= $(shell git rev-parse HEAD)
IMAGE := $(IMAGE_REPO):$(IMAGE_TAG)
FRONTEND_DIR ?= ../frontend
BFF_PORT ?= 8080
AUTH_PORT ?= 8081
FRONTEND_PORT ?= 3000
CAC_OUTPUT_DIR ?= ../../.build/cac
PKL_BIN ?= $(shell if command -v pkl >/dev/null 2>&1 && pkl --version 2>/dev/null | grep -Eq '^Pkl 0\.32\.1([[:space:]]|$$)'; then command -v pkl; else printf '%s' '$(abspath ../../.build/tools/pkl)'; fi)
export PKL_BIN
export PATH := $(dir $(PKL_BIN)):$(PATH)
LOCAL_ENV := ../../scripts/local-cloud-env.sh
LOCAL_SERVICES := ../../scripts/local-services.py
LOCAL_DEMO_CONFIG_PATH ?= $(abspath $(CAC_OUTPUT_DIR)/local/local_demo.json)
LOCAL_DEMO_CONFIG_ENV := LOCAL_DEMO_CONFIG_PATH="$(LOCAL_DEMO_CONFIG_PATH)"
LOCAL_FIXTURE_CONFIG := $(abspath $(CAC_OUTPUT_DIR)/local/local_fixture.yaml)
SYNTO_VERSION := 0.7.0
SYNTO_ARTIFACT_URL := https://files.pythonhosted.org/packages/4a/e9/41c6b61338d98820780a43ed075cd77525674c38242110435330771d771b/synto-0.7.0-py3-none-any.whl
SYNTO_SHA256 := 4bc8dcf14b53f45fac32ce737ecf878f1a46d6d0b010c7decbe6c3b7b10afa77
TEST_ENV := env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY

.PHONY: help ensure-pkl docker-build docker-build-export-job docker-push deploy deploy-dev deploy-prod all build-sync setup local-config local-fixture-config auth-config-local local-synto-runtime local-synto-runtime-test local-worker-build local-fixture local-start dev support-bff support-frontend support-pipeline bff-local auth-local frontend-local local-stop kill-local pipeline-test test-flash-execution vet test build

help:
	@printf '%s\n' \
	  'setup              Install Go/frontend/pinned Synto dependencies and configure localhost URLs' \
	  'local-start        Build worker, ensure missing fixture account, start Auth/BFF/Frontend' \
	  'support-bff        Start Auth + Frontend for a BFF debugger session' \
	  'support-frontend   Start Auth + BFF for a Frontend debugger session' \
	  'support-pipeline   Start all apps; use pipeline-test for no-provider worker tests' \
	  'auth-local         Start Auth with the generated local Auth config file' \
	  'local-stop         Stop only processes supervised for this worktree' \
	  'pipeline-test      Run worker tests without calling a paid provider'

docker-build:
	docker build -t $(IMAGE) .

docker-build-export-job:
	docker build -f Dockerfile.exportjob -t $(IMAGE_REPO)-export-job:$(IMAGE_TAG) .

docker-push:
	docker push $(IMAGE)

deploy: deploy-dev

deploy-dev:
	gcloud run deploy llm-wiki-bff-dev --project llm-wiki-cloud --image $(IMAGE) --region asia-east1 --platform managed --allow-unauthenticated --service-account lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com --update-secrets "JWT_SECRET=jwt-secret-dev:latest,DEEPSEEK_API_KEY=deepseek-apikey:***" --remove-env-vars "QUERY_EXPANSION_MODEL,QUERY_EXPANSION_REASONING,ANSWER_SYNTHESIS_MODEL,ANSWER_SYNTHESIS_REASONING,QUERY_SELECTION_LIMIT,QUERY_SELECTION_EXPLORATION_SLOTS,QUERY_SELECTION_EVIDENCE_THRESHOLD,QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT,QUERY_EXPANSION_ATTEMPTS,QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY" --update-env-vars "^@^GCP_PROJECT=llm-wiki-cloud@BUCKET=llm-wiki-data-dev@FIRESTORE_DATABASE_ID=llm-wiki-cloud-dev@PIPELINE_JOB_URL=https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline-dev:run@ALLOWED_ORIGINS=https://wiki.dev.rayer.idv.tw,https://llm-wiki-frontend-dev.vercel.app,http://localhost:3000,http://127.0.0.1:3000@AUTH_SERVICE_URL=https://auth.dev.rayer.idv.tw@QUERY_STAGE_CONFIG_PATH=/app/configs/query/dev/query-dev-2026-09-12.1.json@DEV_JWT=false" --port 8080

deploy-prod:
	@echo "Production deploys are release-gated. Run the Promote BFF to Cloud Run (production) GitHub workflow with a verified full commit SHA."
	@exit 1

all: docker-build docker-push deploy-dev

build-sync:
	go build -o lwc-sync ./cmd/sync/

ensure-pkl:
	bash ../../scripts/ensure-pkl.sh "$(PKL_BIN)"

setup: ensure-pkl local-config local-synto-runtime
	go mod download
	cd "$(FRONTEND_DIR)" && NODE_ENV=development npm ci --include=dev

vet:
	go vet ./...

test: ensure-pkl
	$(TEST_ENV) python3 ../../scripts/test_cd_contract.py
	$(TEST_ENV) python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'
	$(TEST_ENV) python3 ../../apps/bff/scripts/test_local_dev_makefile.py
	$(TEST_ENV) go test ./... -v -count=1 -race

build:
	go build ./...

local-config: local-fixture-config
	$(MAKE) -C ../.. config-local CONFIG_TARGET=frontend CAC_OUTPUT_DIR="$(abspath $(CAC_OUTPUT_DIR))" BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)"
	BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" $(LOCAL_DEMO_CONFIG_ENV) $(LOCAL_ENV) --configure-frontend "$(FRONTEND_DIR)"
	BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" $(LOCAL_DEMO_CONFIG_ENV) $(LOCAL_ENV) --summary

auth-config-local: ensure-pkl
	LWC_REPOSITORY_ROOT="$(abspath ../..)" go run ./cmd/pipeline_config prepare --target auth --environment local --output "$(CAC_OUTPUT_DIR)/local"

local-fixture-config: ensure-pkl
	mkdir -p "$(CAC_OUTPUT_DIR)/local"
	"$(PKL_BIN)" eval --multiple-file-output-path "$(CAC_OUTPUT_DIR)/local" ../../deploy/cac/local_fixture.pkl

local-synto-runtime:
	@set -eu; \
	state_dir="$$($(LOCAL_ENV) --state-dir)"; \
	venv="$$state_dir/python"; \
	if [ ! -x "$$venv/bin/python3" ]; then python3 -m venv "$$venv"; fi; \
	if ! "$$venv/bin/python3" -c 'import synto; raise SystemExit(synto.__version__ != "$(SYNTO_VERSION)")' >/dev/null 2>&1; then \
	  PIP_CONFIG_FILE=/dev/null PIP_INDEX_URL=https://pypi.org/simple "$$venv/bin/python3" -m pip install --disable-pip-version-check --no-input --no-cache-dir --force-reinstall "$(SYNTO_ARTIFACT_URL)#sha256=$(SYNTO_SHA256)"; \
	fi; \
	"$$venv/bin/python3" -c 'import sys, synto; assert synto.__version__ == "$(SYNTO_VERSION)"; print("Synto", synto.__version__, "Python", sys.version.split()[0], "at", sys.executable)'; \
	test "$$("$$venv/bin/synto" --version)" = "synto, version $(SYNTO_VERSION)"

local-synto-runtime-test: local-synto-runtime
	@env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY \
	  BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" \
	  $(LOCAL_ENV) -- go test -tags lwc_local_synto_runtime ./cmd/olw_worker -run '^TestLocalSyntoRuntime' -count=1 -v

local-worker-build:
	worker="$$($(LOCAL_ENV) --worker-path)"; mkdir -p "$$(dirname "$$worker")"; go build -o "$$worker" ./cmd/olw_worker

local-fixture: local-config
	BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" $(LOCAL_DEMO_CONFIG_ENV) $(LOCAL_ENV) -- go run ./cmd/local_fixture --config "$(LOCAL_FIXTURE_CONFIG)"

local-start: local-config local-synto-runtime
	$(MAKE) local-worker-build BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)"
	$(MAKE) local-fixture BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)"
	LOCAL_CLOUD_PIPELINE_CONFIG_DIR="$(abspath $(CAC_OUTPUT_DIR)/local)" $(LOCAL_DEMO_CONFIG_ENV) BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" $(LOCAL_ENV) -- python3 $(LOCAL_SERVICES) start auth bff frontend

dev: local-start

support-bff: local-config
	$(MAKE) local-worker-build BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)"
	$(MAKE) local-fixture BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)"
	$(LOCAL_DEMO_CONFIG_ENV) BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" $(LOCAL_ENV) -- python3 $(LOCAL_SERVICES) start auth frontend

support-frontend: local-config local-synto-runtime
	$(MAKE) local-worker-build BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)"
	$(MAKE) local-fixture BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)"
	LOCAL_CLOUD_PIPELINE_CONFIG_DIR="$(abspath $(CAC_OUTPUT_DIR)/local)" BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" $(LOCAL_ENV) -- python3 $(LOCAL_SERVICES) start auth bff

support-pipeline: local-start

bff-local: local-config local-synto-runtime local-worker-build local-fixture
	LOCAL_CLOUD_PIPELINE_CONFIG_DIR="$(abspath $(CAC_OUTPUT_DIR)/local)" $(LOCAL_DEMO_CONFIG_ENV) BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" PORT="$(BFF_PORT)" $(LOCAL_ENV) -- go run ./cmd/bff

auth-local: local-config local-fixture
	$(LOCAL_DEMO_CONFIG_ENV) BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" PORT="$(AUTH_PORT)" $(LOCAL_ENV) -- go run ./cmd/auth

frontend-local: local-config
	BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" $(LOCAL_ENV) -- sh -c 'cd "$(FRONTEND_DIR)" && NODE_ENV=development npm run dev -- --hostname localhost --port "$$FRONTEND_PORT"'

local-stop kill-local:
	$(LOCAL_ENV) -- python3 $(LOCAL_SERVICES) stop

pipeline-test:
	go test ./cmd/olw_worker

.PHONY: test-flash-execution
test-flash-execution:
	sh scripts/test_flash_execution.sh
