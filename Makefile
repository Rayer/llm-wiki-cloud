BFF_DIR := apps/bff
FRONTEND_DIR := apps/frontend
CAC_OUTPUT_DIR ?= $(CURDIR)/.build/cac
TEST_ENV := env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY

.PHONY: all help bootstrap lint typecheck vet test build local-start dev local-stop stop smoke workflow-yaml config-local config-dev config-prod verify

all: verify

help:
	@printf '%s\n' \
	  'bootstrap      Install app dependencies and write frontend local config' \
	  'local-start    Start native Auth, BFF, and Frontend against local GCS/Firestore' \
	  'local-stop     Stop managed local processes for this worktree' \
	  'config-local   Render local Pipeline TOML and private bindings in this worktree' \
	  'smoke          Run loopback/auth-boundary and cloud-scope smoke tests' \
	  'lint typecheck test build vet verify  Run repository checks'

bootstrap:
	$(MAKE) -C $(BFF_DIR) setup

lint:
	npm --prefix $(FRONTEND_DIR) run lint

typecheck:
	npm --prefix $(FRONTEND_DIR) run typecheck

vet:
	$(MAKE) -C $(BFF_DIR) vet

test:
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

smoke:
	bash scripts/local-vertical-smoke.sh

workflow-yaml:
	ruby -e 'require "yaml"; files=%w[ci.yml cd.yml deploy-dev.yml promote-production.yml]; workflows=files.to_h { |file| [file, YAML.load_file(".github/workflows/"+file)] }; abort "invalid CI workflow" unless workflows["ci.yml"]["jobs"].key?("bff"); abort "invalid config-only workflow branch" unless workflows["cd.yml"]["jobs"].key?("pipeline-config-only") && workflows["cd.yml"]["jobs"]["release"]["if"].include?("config-only"); files.each { |file| puts ".github/workflows/#{file}: valid YAML" }'

config-local:
	cd $(BFF_DIR) && LWC_REPOSITORY_ROOT="$(CURDIR)" go run ./cmd/pipeline_config prepare --environment local --output "$(CAC_OUTPUT_DIR)/local"

config-dev:
	cd $(BFF_DIR) && LWC_REPOSITORY_ROOT="$(CURDIR)" go run ./cmd/pipeline_config prepare --environment dev --output "$(CAC_OUTPUT_DIR)/dev"

config-prod:
	cd $(BFF_DIR) && LWC_REPOSITORY_ROOT="$(CURDIR)" go run ./cmd/pipeline_config prepare --environment prod --output "$(CAC_OUTPUT_DIR)/prod"

verify: bootstrap workflow-yaml lint typecheck vet test build smoke
	git diff --check
