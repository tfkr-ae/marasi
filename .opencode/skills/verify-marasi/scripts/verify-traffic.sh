#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
ARTIFACT_ROOT="${VERIFY_ARTIFACT_ROOT:-$ROOT/.opencode/verification-artifacts/verify-marasi}"
EVIDENCE_DIR="$ARTIFACT_ROOT/$RUN_ID"
SCRATCH_DIR="$(mktemp -d /tmp/mv.XXXXXX)"
CONFIG_DIR="$SCRATCH_DIR"
INSTANCE="v$$"
PROJECT="verify-$RUN_ID"
BINARY="$ROOT/dist/marasi"
ORIGIN_PID=""
EVENTS_PID=""
SERVICE_STARTED=0

mkdir -p "$EVIDENCE_DIR" "$SCRATCH_DIR/origin"
printf 'marasi verification run %s\n' "$RUN_ID" >"$EVIDENCE_DIR/actions.log"

cleanup() {
  local status=$?
  local cleanup_status
  trap - EXIT INT TERM
  set +e
  if [[ -n "$EVENTS_PID" ]]; then
    kill -INT "$EVENTS_PID" 2>/dev/null
    wait "$EVENTS_PID" 2>/dev/null
    EVENTS_PID=""
  fi
  if [[ "$SERVICE_STARTED" == 1 ]]; then
    "$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service stop --json >"$EVIDENCE_DIR/cleanup.json" 2>"$EVIDENCE_DIR/cleanup.stderr"
    cleanup_status=$?
    if [[ $cleanup_status -ne 0 && $status -eq 0 ]]; then
      status=$cleanup_status
    fi
    cp "$CONFIG_DIR/instances/$INSTANCE.log" "$EVIDENCE_DIR/service.log"
    cleanup_status=$?
    if [[ $cleanup_status -ne 0 && $status -eq 0 ]]; then
      status=$cleanup_status
    fi
  fi
  if [[ -n "$ORIGIN_PID" ]]; then
    kill "$ORIGIN_PID" 2>/dev/null
    wait "$ORIGIN_PID" 2>/dev/null
  fi
  rm -rf "$SCRATCH_DIR"
  cleanup_status=$?
  if [[ $cleanup_status -ne 0 && $status -eq 0 ]]; then
    status=$cleanup_status
  fi
  if [[ $status -eq 0 ]]; then
    printf 'PASS\n' >"$EVIDENCE_DIR/result.txt"
    printf 'verification passed; evidence: %s\n' "$EVIDENCE_DIR"
  else
    printf 'FAIL exit=%d\n' "$status" >"$EVIDENCE_DIR/result.txt"
    printf 'verification failed; evidence: %s\n' "$EVIDENCE_DIR" >&2
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$ROOT"
printf 'GOWORK=off make build VERSION=verify-%s\n' "$RUN_ID" >>"$EVIDENCE_DIR/actions.log"
GOWORK=off make build VERSION="verify-$RUN_ID" >"$EVIDENCE_DIR/build.log" 2>&1

printf '%q ' "$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service start --project-name "$PROJECT" --address 127.0.0.1 --port 0 --json >>"$EVIDENCE_DIR/actions.log"
printf '\n' >>"$EVIDENCE_DIR/actions.log"
"$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service start --project-name "$PROJECT" --address 127.0.0.1 --port 0 --json >"$EVIDENCE_DIR/launch.json" 2>"$EVIDENCE_DIR/launch.stderr"
SERVICE_STARTED=1

printf '%q ' "$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service status --json >>"$EVIDENCE_DIR/actions.log"
printf '\n' >>"$EVIDENCE_DIR/actions.log"
"$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" service status --json >"$EVIDENCE_DIR/doctor.json"

PROXY_LISTENER="$(python3 - "$EVIDENCE_DIR/doctor.json" "$RUN_ID" "$INSTANCE" "$PROJECT" <<'PY'
import json
import os
import sys

path, run_id, instance, project = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    status = json.load(handle)
assert status["status"] == "running", status
assert status["version"] == f"verify-{run_id}", status
assert status["instance"] == instance, status
assert os.path.basename(status["project"]) == f"{project}.marasi", status
assert os.path.isabs(status["project"]), status
listener = status["proxy_listener"]
assert isinstance(listener, str) and listener.startswith("127.0.0.1:"), status
print(listener)
PY
)"

printf 'marasi verification body %s\n' "$RUN_ID" >"$SCRATCH_DIR/origin/proof.txt"
python3 -u - "$SCRATCH_DIR/origin" "$SCRATCH_DIR/origin-port" >"$EVIDENCE_DIR/origin.log" 2>&1 <<'PY' &
import http.server
import os
import sys

directory, port_file = sys.argv[1:]
handler = lambda *args, **kwargs: http.server.SimpleHTTPRequestHandler(*args, directory=directory, **kwargs)
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
with open(port_file, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_port))
server.serve_forever()
PY
ORIGIN_PID=$!

for _ in {1..100}; do
  [[ -s "$SCRATCH_DIR/origin-port" ]] && break
  sleep 0.02
done
[[ -s "$SCRATCH_DIR/origin-port" ]]
ORIGIN_PORT="$(cat "$SCRATCH_DIR/origin-port")"
URL="http://127.0.0.1:$ORIGIN_PORT/proof.txt"

: >"$EVIDENCE_DIR/events-stdout.txt"
: >"$EVIDENCE_DIR/events-stderr.txt"
printf '%q ' "$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" events >>"$EVIDENCE_DIR/actions.log"
printf '\n' >>"$EVIDENCE_DIR/actions.log"
"$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" events >"$EVIDENCE_DIR/events-stdout.txt" 2>"$EVIDENCE_DIR/events-stderr.txt" &
EVENTS_PID=$!

python3 - "$EVIDENCE_DIR/events-stderr.txt" <<'PY'
import pathlib
import sys
import time

path = pathlib.Path(sys.argv[1])
deadline = time.time() + 10
text = ""
while time.time() < deadline:
    text = path.read_text(encoding="utf-8") if path.exists() else ""
    if ": connected" in text:
        sys.exit(0)
    time.sleep(0.05)
sys.stderr.write(f"events command did not connect; stderr={text!r}\n")
sys.exit(1)
PY

printf 'curl --noproxy %q --proxy %q %q\n' '' "http://$PROXY_LISTENER" "$URL" >>"$EVIDENCE_DIR/actions.log"
curl --fail --silent --show-error --noproxy '' --proxy "http://$PROXY_LISTENER" "$URL" --dump-header "$EVIDENCE_DIR/response-headers.txt" --output "$EVIDENCE_DIR/response-body.txt"
cmp "$SCRATCH_DIR/origin/proof.txt" "$EVIDENCE_DIR/response-body.txt"

python3 - "$EVIDENCE_DIR/events-stdout.txt" <<'PY'
import pathlib
import sys
import time

path = pathlib.Path(sys.argv[1])
deadline = time.time() + 10
text = ""
while time.time() < deadline:
    text = path.read_text(encoding="utf-8") if path.exists() else ""
    if "traffic.request" in text and "traffic.response" in text:
        sys.exit(0)
    time.sleep(0.05)
sys.stderr.write(f"events stdout missing traffic frames; stdout={text!r}\n")
sys.exit(1)
PY

printf '%q ' "$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" traffic list --path /proof.txt --status-code 200 --limit 1 --json >>"$EVIDENCE_DIR/actions.log"
printf '\n' >>"$EVIDENCE_DIR/actions.log"
"$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" traffic list --path /proof.txt --status-code 200 --limit 1 --json >"$EVIDENCE_DIR/traffic-list.json"

TRAFFIC_ID="$(python3 - "$EVIDENCE_DIR/traffic-list.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    page = json.load(handle)
assert len(page["items"]) == 1, page
item = page["items"][0]
assert item["method"] == "GET", item
assert item["path"] == "/proof.txt", item
assert item["status_code"] == 200, item
print(item["id"])
PY
)"

printf '%q ' "$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" traffic get "$TRAFFIC_ID" --json >>"$EVIDENCE_DIR/actions.log"
printf '\n' >>"$EVIDENCE_DIR/actions.log"
"$BINARY" --config-dir "$CONFIG_DIR" --instance "$INSTANCE" traffic get "$TRAFFIC_ID" --json >"$EVIDENCE_DIR/traffic-detail.json"

python3 - "$EVIDENCE_DIR/traffic-detail.json" "$TRAFFIC_ID" "$RUN_ID" <<'PY'
import base64
import json
import sys

path, traffic_id, run_id = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    detail = json.load(handle)
assert detail["id"] == traffic_id, detail
assert detail["request"]["method"] == "GET", detail
assert detail["request"]["path"] == "/proof.txt", detail
assert detail["response"]["status_code"] == 200, detail
raw_response = base64.b64decode(detail["response"]["raw"])
assert f"marasi verification body {run_id}".encode() in raw_response, raw_response
PY

python3 - "$EVIDENCE_DIR/events-stdout.txt" "$EVIDENCE_DIR/events-stderr.txt" "$TRAFFIC_ID" "$ORIGIN_PORT" <<'PY'
import json
import sys

stdout_path, stderr_path, traffic_id, origin_port = sys.argv[1:]
stderr = open(stderr_path, encoding="utf-8").read()
assert stderr == ": connected\n", stderr
request_event = None
response_event = None
for raw in open(stdout_path, encoding="utf-8"):
    line = raw.rstrip("\n")
    if not line:
        continue
    name, _, data = line.partition(" ")
    payload = json.loads(data)
    if name == "traffic.request":
        request_event = payload
    elif name == "traffic.response":
        response_event = payload
assert request_event is not None, "missing traffic.request"
assert response_event is not None, "missing traffic.response"
assert request_event["id"] == traffic_id, request_event
assert response_event["id"] == traffic_id, response_event
assert request_event["method"] == "GET", request_event
assert "/proof.txt" in request_event["path"], request_event
assert request_event["host"] == f"127.0.0.1:{origin_port}", request_event
assert response_event["status_code"] == 200, response_event
PY

PROJECT_DB="$CONFIG_DIR/projects/$PROJECT.marasi"
python3 - "$PROJECT_DB" "$TRAFFIC_ID" >"$EVIDENCE_DIR/database-state.txt" <<'PY'
import os
import sqlite3
import sys

path, traffic_id = sys.argv[1:]
assert os.path.getsize(path) > 0, path
connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
row = connection.execute(
    "SELECT id, method, path, status_code FROM request WHERE id = ?", (traffic_id,)
).fetchone()
connection.close()
assert row == (traffic_id, "GET", "/proof.txt", 200), row
print(f"project={path}")
print(f"bytes={os.path.getsize(path)}")
print(f"request_row={row!r}")
PY
