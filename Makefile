PROJECT  := llm-wiki-cloud
REGION   := asia-east1
IMAGE    := gcr.io/$(PROJECT)/olw-worker
JOB      := olw-worker
WDOG_JOB := lock-watchdog
BUCKET   := llm-wiki-data
USER_ID  := test-user
PROJ_ID  := test-project

.PHONY: build deploy watchdog-deploy run logs status clean all

# ── Build & Push ────────────────────────────────────
build:
	gcloud builds submit --tag $(IMAGE) --timeout=600 .

# ── Deploy worker ────────────────────────────────────
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
		--memory 1Gi \
		--task-timeout 1800 \
		--max-retries 1 \
		--set-env-vars "GCP_PROJECT=$(PROJECT),BUCKET=$(BUCKET),USER_ID=$(USER_ID),PROJECT_ID=$(PROJ_ID)"

# ── Deploy watchdog ──────────────────────────────────
watchdog-deploy:
	gcloud run jobs delete $(WDOG_JOB) --region $(REGION) --quiet 2>/dev/null || true
	gcloud run jobs create $(WDOG_JOB) \
		--image $(IMAGE) \
		--region $(REGION) \
		--memory 512Mi \
		--task-timeout 120 \
		--max-retries 1 \
		--command python3 \
		--args /watchdog.py

watchdog-run:
	gcloud run jobs execute $(WDOG_JOB) --region $(REGION) --wait

# ── Run ──────────────────────────────────────────────
run:
	gcloud run jobs execute $(JOB) --region $(REGION) --wait

# ── Logs ─────────────────────────────────────────────
logs:
	@gcloud run jobs executions list --job=$(JOB) --region=$(REGION) --limit=5 2>&1; \
	echo; \
	echo "Usage: make logs-id ID=<execution-id>"

logs-id:
	gcloud logging read \
		"resource.type=cloud_run_job AND labels.\"run.googleapis.com/execution_name\"=$(ID) \
		--limit=40 --format="table(textPayload)"

# ── Clean ────────────────────────────────────────────
clean:
	gcloud run jobs delete $(JOB) --region $(REGION) --quiet
	gcloud run jobs delete $(WDOG_JOB) --region $(REGION) --quiet

# ── Status ───────────────────────────────────────────
status:
	@echo "=== GCS ==="
	@gsutil ls -r gs://$(BUCKET)/users/$(USER_ID)/projects/$(PROJ_ID)/ 2>&1 | tail -10
	@echo
	@echo "=== Cloud Run Jobs ==="
	@gcloud run jobs describe $(JOB) --region $(REGION) --format="table(name,status.conditions)" 2>&1 | head -4
	@echo
	@echo "=== Recent executions ==="
	@gcloud run jobs executions list --job=$(JOB) --region=$(REGION) --limit=3 2>&1

# ── Full cycle ───────────────────────────────────────
all: build deploy run
