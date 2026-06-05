# LLM Wiki Cloud

> OLW pipeline on GCP Cloud Run — Gemini API, Firestore lock, GCS state.

## Quick Start

```bash
cd ~/Documents/Develop/llm-wiki-cloud

# Build & deploy
make all           # build → deploy → run

# Or step by step
make build         # Docker build + push to GCR
make deploy        # Update Cloud Run Job
make run           # Execute + wait for completion
make logs-id ID=<execution-id>  # View logs
```

## Architecture

```
User uploads .md  →  GCS (raw/)  →  Cloud Run Job (olw-worker)
                                        │
                                   Firestore lock
                                   Secret Manager (Gemini key)
                                        │
                                   OLW: ingest → compile → publish
                                        │
                                   GCS (wiki/ + .olw/)
                                        │
                                   Frontend (TBD)
```

## Commands

| Command | Does |
|---|---|
| `make build` | Docker build + push to GCR |
| `make deploy` | Update worker image + timeout |
| `make deploy-fresh` | Full recreate (env vars changed) |
| `make run` | Execute worker, wait for completion |
| `make logs` | List recent executions |
| `make logs-id ID=<id>` | View execution logs |
| `make status` | GCS + Cloud Run status |
| `make watchdog-deploy` | Deploy lock watchdog |
| `make watchdog-run` | Run watchdog once |
| `make all` | build → deploy → run |

## GCP Resources

| Resource | Name | Notes |
|---|---|---|
| Project | `llm-wiki-cloud` | asia-east1 |
| GCS | `gs://llm-wiki-data` | users/{uid}/projects/{pid}/ |
| Firestore | `(default)` | locks collection, free tier |
| Secret Manager | `gemini-api-key` | Read by worker at runtime |
| Cloud Run Job | `olw-worker` | 1Gi, 1800s timeout |
| Cloud Run Job | `lock-watchdog` | 512Mi, 120s timeout |

## Files

| File | Purpose |
|---|---|
| `worker.py` | Main pipeline: lock → sync → OLW → sync → unlock |
| `watchdog.py` | Stale lock cleaner (cron every 5-10 min) |
| `Dockerfile` | Python 3.12 + OLW (pip) + gcloud |
| `Makefile` | Build / deploy / run / logs |

## Setup (one-time)

```bash
# 1. Enable APIs
gcloud services enable firestore.googleapis.com aiplatform.googleapis.com \
    secretmanager.googleapis.com --project=llm-wiki-cloud

# 2. Create Firestore (native mode)
gcloud firestore databases create --project=llm-wiki-cloud \
    --location=asia-east1 --type=firestore-native

# 3. Store Gemini API key
echo "YOUR_GEMINI_KEY" | gcloud secrets create gemini-api-key \
    --data-file=- --project=llm-wiki-cloud

# 4. Grant service account access
gcloud secrets add-iam-policy-binding gemini-api-key \
    --member="serviceAccount:580854833715-compute@developer.gserviceaccount.com" \
    --role="roles/secretmanager.secretAccessor" \
    --project=llm-wiki-cloud

# 5. Deploy
make deploy-fresh
make watchdog-deploy
```

## Design Docs

Obsidian vault: `Helios/Projects/LLM-Wiki-Cloud/`
- `design.md` — Full architecture (tenants, storage, pipeline, query API, deployment)
- `handoff.md` — Session notes, debug history, decisions

## Status (2026-06-03)

- ✅ Lock (Firestore TTL + watchdog)
- ✅ GCS sync (raw/wiki/.olw)
- ✅ OLW ingest (3 test articles)
- ✅ OLW compile (3 source articles → wiki/sources/)
- ✅ Gemini integration (Secret Manager)
- ✅ Concept drafts (2 drafts in wiki/.drafts/ — Durable Workflows, End-to-end brittleness)
- ⚠️ Compile (Gemini 429 rate limit — heavy/flash both flash-only; 2-3 concepts per run)
- ⚠️ auto_maintain enabled, auto_commit disabled (git not in container)
- ⚠️ GCS Content-Type fixed: `text/markdown; charset=utf-8`
- 🧪 Frontend mockup (`frontend-mockup.html`) — static HTML with real mock data

## Frontend Mockup

`frontend-mockup.html` — 零依賴靜態 HTML，對齊真實 GCS 狀態：

| Feature | Status |
|---|---|
| 1. Query (wiki-only / wiki+LLM modes) | ✅ mock search engine + AI synthesis |
| 2. Wiki concepts list (sidebar) | ✅ 2 drafts listed, +8 pending |
| 3. Compiled sources list (sidebar) | ✅ 3 sources, click → render |
| 4. Bookmark import placeholder | ✅ POC placeholder, Phase 2 |
| 5. Wiki render with internal links | ✅ `[[wikilinks]]` clickable, cross-page navigation |
| 6. Query results → wiki source links | ✅ each result links back to source page |
