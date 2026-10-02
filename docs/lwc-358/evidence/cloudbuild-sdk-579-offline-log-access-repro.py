#!/usr/bin/env python3
"""TEST ONLY: exercise installed Cloud Build SDK completion/log error path offline."""

from __future__ import annotations

import json
import sys
import threading
from pathlib import Path
from types import SimpleNamespace


SDK_ROOT = Path("/opt/homebrew/share/google-cloud-sdk")
sys.path.extend(
    [str(SDK_ROOT / "lib"), str(SDK_ROOT / "lib" / "third_party")]
)

from apitools.base.py import exceptions as api_exceptions  # noqa: E402
from googlecloudsdk.api_lib.cloudbuild import logs  # noqa: E402


class FakeTransport:
    requests = 0
    stop_event = threading.Event()

    def __init__(self):
        pass

    def Request(self, _url, _cursor):
        type(self).requests += 1
        if type(self).requests == 1:
            if not type(self).stop_event.wait(timeout=3):
                raise AssertionError("fake build did not reach terminal status")
            return logs.Response(416, {}, b"")
        raise api_exceptions.HttpError(
            {"status": 403}, b"synthetic offline denial", "https://offline.invalid/log"
        )


class FakeMessages:
    class BuildOptions:
        class LoggingValueValuesEnum:
            NONE = "NONE"
            STACKDRIVER_ONLY = "STACKDRIVER_ONLY"
            CLOUD_LOGGING_ONLY = "CLOUD_LOGGING_ONLY"

    class Build:
        class StatusValueValuesEnum:
            QUEUED = "QUEUED"
            WORKING = "WORKING"

    @staticmethod
    def CloudbuildProjectsLocationsBuildsGetRequest(name):
        return SimpleNamespace(name=name)


class FakeBuildApi:
    def __init__(self):
        self.projects_locations_builds = self
        self.statuses = iter(("WORKING", "SUCCESS"))
        self.status_reads = 0
        self.final_status = None

    def Get(self, _request):
        self.status_reads += 1
        build = SimpleNamespace(
            id="offline-build",
            projectId="offline-project",
            status=next(self.statuses),
            options=SimpleNamespace(logging="LEGACY"),
            logsBucket="gs://offline-bucket",
        )
        self.final_status = build.status
        return build


class FakeBuildRef:
    id = "offline-build"
    projectId = "offline-project"

    @staticmethod
    def Collection():
        return "cloudbuild.projects.locations.builds"

    @staticmethod
    def RelativeName():
        return "projects/offline-project/locations/global/builds/offline-build"


def main():
    # Replace the only log-network boundary. The Cloud Build API is a local fake too.
    original_transport = logs.RequestsLogTailer
    original_sleep = logs.time.sleep
    original_stop = logs.GCSLogTailer.Stop
    logs.RequestsLogTailer = FakeTransport
    logs.time.sleep = lambda _seconds: None
    FakeTransport.stop_event = threading.Event()

    def signal_stopped(tailer):
        original_stop(tailer)
        FakeTransport.stop_event.set()

    logs.GCSLogTailer.Stop = signal_stopped
    api = FakeBuildApi()
    raised = None
    returned_status = None
    try:
        client = logs.CloudBuildClient(
            client=api, messages=FakeMessages, polling_interval=0
        )
        try:
            returned_status = client.Stream(FakeBuildRef(), out=None).status
        except Exception as exc:  # The SDK maps the fake HTTP 403 on its real thread path.
            raised = type(exc).__name__
    finally:
        logs.RequestsLogTailer = original_transport
        logs.time.sleep = original_sleep
        logs.GCSLogTailer.Stop = original_stop

    if api.final_status != "SUCCESS" or returned_status is not None:
        raise AssertionError(
            f"unexpected return path: api={api.final_status!r}, returned={returned_status!r}"
        )
    if raised != "DefaultLogsBucketIsOutsideSecurityPerimeterException":
        raise AssertionError(f"unexpected SDK outcome: {raised!r}")
    if FakeTransport.requests != 2 or api.status_reads != 2:
        raise AssertionError(
            f"unexpected fake call counts: log={FakeTransport.requests}, "
            f"status={api.status_reads}"
        )

    version = (SDK_ROOT / "VERSION").read_text().strip()
    print(
        json.dumps(
            {
                "probe": "offline-only; installed SDK CloudBuildClient with fake build API and fake log transport",
                "sdk_version": version,
                "stream_output_argument_is_none": True,
                "cloud_build_api_final_status": api.final_status,
                "cloud_build_status_reads": api.status_reads,
                "fake_log_transport_requests": FakeTransport.requests,
                "sdk_raised": raised,
            },
            sort_keys=True,
        )
    )


if __name__ == "__main__":
    main()
