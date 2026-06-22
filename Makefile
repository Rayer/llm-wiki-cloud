PROJECT  := llm-wiki-cloud
REGION   := asia-east1
IMAGE    := asia-east1-docker.pkg.dev/$(PROJECT)/cloud-run-images/olw-pipeline
JOB      := olw-pipeline
BUCKET   := llm-wiki-data
USER_ID  := test-user
PROJ_ID  := demo

.PHONY: build deploy run logs status deploy-fresh

build:
	gcloud builds submit --tag $(IMAGE) --project $(PROJECT) --quiet

deploy:
	gcloud run jobs update $(JOB) \
		--project $(PROJECT) \
		--image $(IMAGE) \
		--region $(REGION) \
		--memory 2Gi \
		--task-timeout 1800 \
		--max-retries 1 \
		--add-volume name=wiki-data,type=cloud-storage,bucket=$(BUCKET) \
		--add-volume-mount volume=wiki-data,mount-path=/data \
		--quiet

deploy-fresh:
	gcloud run jobs delete $(JOB) --region $(REGION) --project $(PROJECT) --quiet 2>/dev/null || true
	gcloud run jobs create $(JOB) \
		--project $(PROJECT) \
		--image $(IMAGE) \
		--region $(REGION) \
		--memory 2Gi \
		--task-timeout 1800 \
		--max-retries 1 \
		--add-volume name=wiki-data,type=cloud-storage,bucket=$(BUCKET) \
		--add-volume-mount volume=wiki-data,mount-path=/data \
		--quiet

run:
	gcloud run jobs execute $(JOB) \
		--project $(PROJECT) \
		--region $(REGION) \
		--args run \
		--update-env-vars "USER_ID=$(USER_ID),PROJECT_ID=$(PROJ_ID)" \
		--wait

logs:
	gcloud logging read "resource.type=cloud_run_job AND resource.labels.job_name=$(JOB)" \
		--project $(PROJECT) --freshness=1h --limit=20 --format "value(textPayload)"

status:
	gcloud run jobs executions list --job $(JOB) --region $(REGION) --project $(PROJECT) --limit=5
