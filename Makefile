BFF_DIR := apps/bff
FRONTEND_DIR := apps/frontend
LOCAL_ENV := $(BFF_DIR)/../../scripts/local-cloud-env.sh
CAC_OUTPUT_DIR ?= $(CURDIR)/.build/cac
CAC_TARGET ?= pipeline
BFF_PORT ?= 8080
AUTH_PORT ?= 8081
FRONTEND_PORT ?= 3000
PKL_BIN ?= $(shell if command -v pkl >/dev/null 2>&1 && pkl --version 2>/dev/null | grep -Eq '^Pkl 0\.32\.1([[:space:]]|$$)'; then command -v pkl; else printf '%s' '$(CURDIR)/.build/tools/pkl'; fi)
export PKL_BIN
export PATH := $(dir $(PKL_BIN)):$(PATH)
CAC_TARGET ?= pipeline
TEST_ENV := env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY

.PHONY: all help ensure-pkl bootstrap lint typecheck vet test build local-start dev local-stop stop smoke workflow-yaml config-local config-dev config-prod verify

all: verify

help:
	@printf '%s\n' \
	  'bootstrap      Install app dependencies and write frontend local config' \
	  'local-start    Start native Auth, BFF, and Frontend against local GCS/Firestore' \
	  'local-stop     Stop managed local processes for this worktree' \
	  'config-local   Render Pipeline, BFF, or Auth SSOT output (CAC_TARGET=pipeline|bff|auth)' \
	  'smoke          Run loopback/auth-boundary and cloud-scope smoke tests' \
	  'lint typecheck test build vet verify  Run repository checks'

ensure-pkl:
	$(MAKE) -C $(BFF_DIR) ensure-pkl PKL_BIN="$(PKL_BIN)"

bootstrap: ensure-pkl
	$(MAKE) -C $(BFF_DIR) setup

lint:
	npm --prefix $(FRONTEND_DIR) run lint

typecheck:
	npm --prefix $(FRONTEND_DIR) run typecheck

vet:
	$(MAKE) -C $(BFF_DIR) vet

test: ensure-pkl
	$(TEST_ENV) $(MAKE) -C $(BFF_DIR) test
	$(TEST_ENV) npm --prefix $(FRONTEND_DIR) test

build:
	$(MAKE) -C $(BFF_DIR) build
	npm --prefix $(FRONTEND_DIR) run build

local-start:
	$(MAKE) -C $(BFF_DIR) dev

dev: local-start

local-stop:
	$(MAKE) -C $(BFF_DIR) kill-local

stop: local-stop

smoke: ensure-pkl
	bash scripts/local-vertical-smoke.sh

workflow-yaml:
	ruby -e 'require "yaml"; files=%w[ci.yml cd.yml deploy-dev.yml promote-production.yml]; workflows=files.to_h { |file| [file, YAML.load_file(".github/workflows/"+file)] }; abort "invalid CI workflow" unless workflows["ci.yml"]["jobs"].key?("bff"); abort "invalid config-only workflow branch" unless workflows["cd.yml"]["jobs"].key?("pipeline-config-only") && workflows["cd.yml"]["jobs"]["release"]["if"].include?("config-only"); files.each { |file| puts ".github/workflows/#{file}: valid YAML" }'

config-local: ensure-pkl
	@if [ "$(CAC_TARGET)" = auth ]; then \
	  BFF_PORT="$(BFF_PORT)" AUTH_PORT="$(AUTH_PORT)" FRONTEND_PORT="$(FRONTEND_PORT)" "$(LOCAL_ENV)" -- $(MAKE) -C $(BFF_DIR) auth-config-local CAC_OUTPUT_DIR="$(CAC_OUTPUT_DIR)" PKL_BIN="$(PKL_BIN)"; \
	else \
	  cd $(BFF_DIR) && PKL_BIN="$(PKL_BIN)" LWC_REPOSITORY_ROOT="$(CURDIR)" go run ./cmd/pipeline_config prepare --target "$(CAC_TARGET)" --environment local --output "$(CAC_OUTPUT_DIR)/local"; \
	fi

config-dev: ensure-pkl
	cd $(BFF_DIR) && PKL_BIN="$(PKL_BIN)" LWC_REPOSITORY_ROOT="$(CURDIR)" go run ./cmd/pipeline_config prepare --target "$(CAC_TARGET)" --environment dev --output "$(CAC_OUTPUT_DIR)/dev"

config-prod: ensure-pkl
	cd $(BFF_DIR) && PKL_BIN="$(PKL_BIN)" LWC_REPOSITORY_ROOT="$(CURDIR)" go run ./cmd/pipeline_config prepare --target "$(CAC_TARGET)" --environment prod --output "$(CAC_OUTPUT_DIR)/prod"

verify: bootstrap workflow-yaml lint typecheck vet test build smoke
	git diff --check
