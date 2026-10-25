#!/usr/bin/env python3
"""Verify the Compose development stack, including database outage/recreation.

This intentionally stops/recreates this project's postgres container. It never
removes the volume and cleans up only its own generated verification schema.
"""
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
os.chdir(ROOT)
DATABASE_URL = os.environ.get("PROJECTMAP_DATABASE_URL", "")
parsed = urllib.parse.urlparse(DATABASE_URL)
if parsed.hostname not in ("127.0.0.1", "localhost") or parsed.port != 15432 or parsed.path != "/projectmap":
    raise SystemExit("Set PROJECTMAP_DATABASE_URL to the Compose projectmap database on localhost:15432.")
DOCKER = shutil.which("docker") or "/Applications/Docker.app/Contents/Resources/bin/docker"
ENV = {**os.environ, "PATH": str(Path(DOCKER).parent) + os.pathsep + os.environ.get("PATH", "")}
BINARY = ROOT / ".local/projectmap"
SCHEMA = "projectmap_verify_" + uuid.uuid4().hex
process = None
schema_created = False
container_stopped = False


def compose(*args):
    subprocess.run([DOCKER, "compose", *args], env=ENV, check=True, stdout=subprocess.DEVNULL)


def cli(command):
    return subprocess.check_output([str(BINARY), command], env=ENV, text=True)


def sql(statement):
    return subprocess.check_output(
        [DOCKER, "compose", "exec", "-T", "postgres", "psql", "-X", "-U", "projectmap", "-d", "projectmap", "-v", "ON_ERROR_STOP=1", "-At"],
        input=statement, env=ENV, text=True,
    ).strip()


def start_backend():
    global process, base_url
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    base_url = "http://127.0.0.1:" + str(port)
    process = subprocess.Popen([str(BINARY), "serve"], env={**ENV, "PROJECTMAP_ADDR": "127.0.0.1:" + str(port)}, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def stop_backend():
    global process
    if process is None:
        return
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
    try:
        code = process.wait(timeout=7)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()
        raise RuntimeError("Backend did not stop gracefully")
    process = None
    if code != 0:
        raise RuntimeError("Backend exited unsuccessfully")


def wait_http(path, expected):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("Backend exited before readiness verification")
        try:
            with urllib.request.urlopen(base_url + path, timeout=3) as response:
                code = response.status
        except urllib.error.HTTPError as error:
            code = error.code
        except (urllib.error.URLError, TimeoutError):
            code = None
        if code == expected:
            print(path + ": " + str(code))
            return
        time.sleep(0.2)
    raise RuntimeError(path + " did not return " + str(expected))


try:
    compose("up", "-d", "--wait", "postgres")
    cli("migrate")
    identities = json.loads(cli("bootstrap"))
    assert json.loads(cli("bootstrap")) == identities, "Bootstrap changed identities"
    sql(f'CREATE SCHEMA "{SCHEMA}"; CREATE TABLE "{SCHEMA}".marker (value text PRIMARY KEY); INSERT INTO "{SCHEMA}".marker VALUES (\'persisted\');')
    schema_created = True
    start_backend()
    wait_http("/healthz", 200)
    wait_http("/readyz", 200)
    compose("stop", "postgres")
    container_stopped = True
    wait_http("/healthz", 200)
    wait_http("/readyz", 503)
    compose("up", "-d", "--wait", "--force-recreate", "postgres")
    container_stopped = False
    wait_http("/readyz", 200)
    assert sql(f'SELECT value FROM "{SCHEMA}".marker;') == "persisted", "Database data did not persist"
    assert json.loads(cli("bootstrap")) == identities, "Identities changed after container recreation"
    print("Database marker and local identities survived container recreation.")
    stop_backend()
    start_backend()
    wait_http("/readyz", 200)
    assert sql(f'SELECT value FROM "{SCHEMA}".marker;') == "persisted"
    print("Backend restart preserved stored data.")
finally:
    try:
        stop_backend()
    finally:
        if container_stopped:
            compose("up", "-d", "--wait", "postgres")
        if schema_created:
            sql(f'DROP SCHEMA "{SCHEMA}" CASCADE;')
