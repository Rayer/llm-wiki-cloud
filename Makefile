BFF_DIR := apps/bff
FRONTEND_DIR := apps/frontend

.PHONY: all bootstrap lint typecheck vet test build local-start dev local-stop stop smoke workflow-yaml verify

all: verify

bootstrap:
	$(MAKE) -C $(BFF_DIR) setup

lint:
	npm --prefix $(FRONTEND_DIR) run lint

typecheck:
	npm --prefix $(FRONTEND_DIR) run typecheck

vet:
	$(MAKE) -C $(BFF_DIR) vet

test:
	$(MAKE) -C $(BFF_DIR) test
	npm --prefix $(FRONTEND_DIR) test

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
	node -e 'const fs=require("fs"); const yaml=require("./$(FRONTEND_DIR)/node_modules/js-yaml"); const workflow=yaml.load(fs.readFileSync(".github/workflows/ci.yml", "utf8")); if (!workflow || !workflow.jobs || !workflow.jobs.build || Object.values(workflow.jobs).some((job) => job.permissions?.statuses === "write")) process.exit(1); console.log(".github/workflows/ci.yml: valid YAML");'

verify: bootstrap workflow-yaml lint typecheck vet test build smoke
	git diff --check
