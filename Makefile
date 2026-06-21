PROJECT  := llm-wiki-cloud
REGION   := asia-east1
IMAGE    := gcr.io/$(PROJECT)/olw-worker
JOB      := olw-pipeline
BUCKET   := llm-wiki-data
USER_ID  := test-user
PROJ_ID  := demo

.PHONY: build deploy deploy-fresh run logs status clean all

# ── Build & Push ────────────────────────────────────
build:
	gcloud builds submit --tag $(IMAGE) --timeout=600 .

# ── Deploy ──────────────────────────────────────────
deploy:
	gcloud run jobs update $(JOB) \
		--region $(REGION) \
		--image $(IMAGE) \
		--task-timeout 1800

deploy-fresh:
	gcloud run jobs delete $(JOB) --region $(REGION) --quiet 2>/dev/null || true
	gcloud run jobs create $(JOB) \
		--image $(IMAGE) \
		--region $(REGION) \
		--memory 2Gi \
		--task-timeout 1800 \
		--max-retries 1 \
		--set-env-vars "BUCKET=$(BUCKET)"

# ── Run ──────────────────────────────────────────────
run:
	gcloud run jobs execute $(JOB) \
		--region $(REGION) \
		--args run \
		--update-env-vars "USER_ID=$(USER_ID),PROJECT_ID=$(PROJ_ID)" \
		--wait

run-cmd:
	gcloud run jobs execute $(JOB) \
		--region $(REGION) \
		--args "$(CMD)" \
		--update-env-vars "USER_ID=$(USER_ID),PROJECT_ID=$(PROJ_ID)" \
		--wait

# ── Logs ─────────────────────────────────────────────
logs:
	@gcloud run jobs executions list --job=$(JOB) --region=$(REGION) --limit=5 2>&1; \
	echo; \
	echo "Usage: make logs-id ID=<execution-id>"

logs-id:
	gcloud logging read \
		"resource.type=cloud_run_job AND labels.\"run.googleapis.com/execution_name\"=$(ID)" \
		--limit=40 --format="table(textPayload)"

# ── Clean ────────────────────────────────────────────
clean:
	gcloud run jobs delete $(JOB) --region $(REGION) --quiet

# ── Status ───────────────────────────────────────────
status:
	@echo "=== GCS ==="
	@gsutil ls -r gs://$(BUCKET)/users/$(USER_ID)/projects/$(PROJ_ID)/wiki/ 2>&1 | tail -10
	@echo
	@echo "=== Cloud Run Job ==="
	@gcloud run jobs describe $(JOB) --region $(REGION) --format="table(name,status.conditions)" 2>&1 | head -4
	@echo
	@echo "=== Recent executions ==="
	@gcloud run jobs executions list --job=$(JOB) --region=$(REGION) --limit=3 2>&1

# ── Full cycle ───────────────────────────────────────
all: build deploy run
