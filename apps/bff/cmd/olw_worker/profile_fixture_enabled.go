//go:build byokprofilefixture

package main

import (
	"os"
)

func profileFixtureEnvironment() []string {
	if os.Getenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT") != "1" {
		return nil
	}
	env := []string{"LWC_PROFILE_FIXTURE_TRANSPORT=1"}
	if receipt := os.Getenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE"); receipt != "" {
		env = append(env, "LWC_PROFILE_FIXTURE_RECEIPT_FILE="+receipt)
	}
	return env
}

func profileFixtureScript(adapter string) string {
	if os.Getenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT") != "1" {
		return adapter
	}
	return profileFixturePythonHook + "\n" + adapter
}

const profileFixturePythonHook = `
import httpx as _lwc_fixture_httpx
import json as _lwc_fixture_json
import os as _lwc_fixture_os
import re as _lwc_fixture_re
import socket as _lwc_fixture_socket
import time as _lwc_fixture_time

def _lwc_fixture_record(protocol, model, path):
    receipt = _lwc_fixture_os.environ.get("LWC_PROFILE_FIXTURE_RECEIPT_FILE", "")
    if not receipt:
        return
    try:
        fd = _lwc_fixture_os.open(receipt, _lwc_fixture_os.O_WRONLY | _lwc_fixture_os.O_CREAT | _lwc_fixture_os.O_APPEND, 0o600)
        with _lwc_fixture_os.fdopen(fd, "a", encoding="utf-8") as stream:
            stream.write(f"{_lwc_fixture_time.time_ns()}\t{protocol}\t{model}\t{path}\n")
    except OSError:
        pass

def _lwc_fixture_content(body):
    messages = body.get("messages", [])
    system = next((m.get("content", "") for m in messages if m.get("role") == "system"), "")
    user = next((m.get("content", "") for m in messages if m.get("role") == "user"), "")
    if "raw_query string" in system and "Criterion" in system:
        match = _lwc_fixture_re.search(r"Raw query: (.*?)(?:\nCriterion policy:|$)", user, _lwc_fixture_re.S)
        raw_query = match.group(1).strip() if match else "disposable profile routing"
        return _lwc_fixture_json.dumps({
            "raw_query": raw_query, "required": [], "excluded": [],
            "preferred": [{"kind": "topic", "value": "BYOK profile routing", "terms": ["BYOK", "profile", "routing"], "proof": "lexical"}],
            "goals": [], "supporting_dimensions": [], "acceptable_alternatives": [], "ambiguity": [], "fallback": False,
        })
    if '"summary"' in system and '"concepts"' in system:
        return '{"summary":"A disposable source describes BYOK profile routing for a Project.","concepts":[{"name":"BYOK Profile Routing","aliases":["BYOK","Project profile"]}],"suggested_topics":["BYOK Profile Routing"],"named_references":[],"quality":"high","language":"en"}'
    if '"articles"' in system and '"mocs_to_update"' in system:
        return '{"articles":[{"title":"BYOK Profile Routing","action":"create","path":"byok-profile-routing.md","reasoning":"The source explains Project profile selection.","source_paths":["raw/lwc-byok-disposable-source.md"]}],"mocs_to_update":[]}'
    if '"content"' in system and '"tags"' in system and '"title"' in system:
        return '{"title":"BYOK Profile Routing","content":"## Project profile routing\\n\\nA Project uses its saved BYOK profile for Query and Pipeline chat calls. The profile keeps its endpoint, model, protocol, and encrypted credential revision together.","tags":["byok","profile-routing"]}'
    if '"terms"' in system and '"definition"' in system:
        return '{"terms":[]}'
    if '"relations"' in system and '"subject"' in system:
        return '{"relations":[]}'
    return "The Project uses its selected BYOK profile and model for this chat call."

def _lwc_fixture_response(request):
    try:
        body = _lwc_fixture_json.loads(request.content or b"{}")
    except Exception:
        return _lwc_fixture_httpx.Response(400, json={"error": {"message": "invalid fixture request"}})
    model = body.get("model") or "fixture-embedding-model"
    if request.url.host == "fixture.example" and request.url.path.endswith("/models"):
        _lwc_fixture_record("openai_models_healthcheck", model, request.url.path)
        return _lwc_fixture_httpx.Response(200, json={"object": "list", "data": []})
    if request.url.path.endswith("/embeddings"):
        _lwc_fixture_record("fixture_embeddings", model, request.url.path)
        return _lwc_fixture_httpx.Response(200, json={"object": "list", "data": [{"object": "embedding", "index": 0, "embedding": [0.0, 0.1, 0.2, 0.3]}], "model": model, "usage": {"prompt_tokens": 1, "total_tokens": 1}})
    if request.url.host != "fixture.example" or not request.url.path.endswith("/chat/completions"):
        return _lwc_fixture_httpx.Response(502, json={"error": {"message": "fixture has no route for this endpoint"}})
    _lwc_fixture_record("openai_chat_completions", model, request.url.path)
    content = _lwc_fixture_content(body)
    return _lwc_fixture_httpx.Response(200, json={"id": "synthetic-pipeline-fixture", "object": "chat.completion", "created": 1, "model": model, "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})

if _lwc_fixture_os.environ.get("LWC_PROFILE_FIXTURE_TRANSPORT") == "1":
    _lwc_fixture_original_getaddrinfo = _lwc_fixture_socket.getaddrinfo
    def _lwc_fixture_getaddrinfo(host, port, family=0, type=0, proto=0, flags=0):
        if isinstance(host, str) and host.rstrip(".").lower() == "fixture.example":
            return [(_lwc_fixture_socket.AF_INET, _lwc_fixture_socket.SOCK_STREAM,
                     _lwc_fixture_socket.IPPROTO_TCP, "", ("93.184.216.34", int(port or 443)))]
        return _lwc_fixture_original_getaddrinfo(host, port, family, type, proto, flags)
    _lwc_fixture_socket.getaddrinfo = _lwc_fixture_getaddrinfo

    _lwc_fixture_original_client_init = _lwc_fixture_httpx.Client.__init__
    def _lwc_fixture_client_init(self, *args, **kwargs):
        kwargs["transport"] = _lwc_fixture_httpx.MockTransport(_lwc_fixture_response)
        _lwc_fixture_original_client_init(self, *args, **kwargs)
    _lwc_fixture_httpx.Client.__init__ = _lwc_fixture_client_init
`
