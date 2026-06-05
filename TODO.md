# TODO

## Worker LLM Provider

- Add `LLM_PROVIDER` support to `worker.py`.
- Keep DeepSeek as the next default provider:
  - provider: `deepseek`
  - URL: `https://api.deepseek.com/v1`
  - Secret Manager name: `deepseek-api-key`
  - default model: `deepseek-chat`
- Keep Gemini/xAI support configurable for later instead of hard-coding one provider path.

## Worker Modes

- Add `WORKER_MODE` support:
  - `run`: current behavior.
  - `clean`: sync from GCS, run `olw clean --yes --vault /data`, sync cleaned wiki/state back to GCS, then exit.
  - `clean-run`: clean first, then run the normal OLW pipeline and sync results back.
- Treat clean modes as destructive operations because they propagate deleted wiki/state data back to GCS.

## Vertex AI Spike

- Investigate whether Vertex AI Gemini can replace the current Gemini API-key path.
- First test should be a minimal local/container prompt using GCP service account auth, region, model name, and project quota.
- Decide after the spike whether OLW can use a Vertex OpenAI-compatible endpoint directly or needs an adapter.
- Consider Vertex AI Agent Engine later for frontend/query agent workflows rather than the OLW compile worker path.
