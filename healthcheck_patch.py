# PYTHONSTARTUP script: patches OLW healthcheck timeout 5s → 30s
# for Cloud Run environments where DNS/SSL is slow on cold start.
try:
    from obsidian_llm_wiki.openai_compat_client import OpenAICompatClient
    _orig = OpenAICompatClient.healthcheck
    def _healthcheck(self):
        try:
            resp = self._client.get(self._models_url(), timeout=30)
            return resp.status_code in (200, 401)
        except Exception:
            return False
    OpenAICompatClient.healthcheck = _healthcheck
except ImportError:
    pass
