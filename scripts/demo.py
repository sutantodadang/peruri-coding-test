"""API demo against real PostgreSQL-backed server. Standard library only."""
import argparse
import concurrent.futures
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:8080")
    parser.add_argument("--principal-url", default="http://127.0.0.1:9000")
    args = parser.parse_args()
    api_token = os.getenv("API_TOKEN", "local-api-token")
    cb_token = os.getenv("CALLBACK_TOKEN", "local-callback-token")
    principal_token = os.getenv("PRINCIPAL_TOKEN", "local-principal-token")
    suffix = uuid.uuid4().hex[:12]

    def call(method, path, body=None, token=api_token, expected=200, base=args.url):
        headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
        req = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
        try:
            response = urllib.request.urlopen(req, timeout=65)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read()
            assert response.code == expected, (method, path, response.code, raw.decode())
            return json.loads(raw) if raw else None

    def create(name, modes):
        no = name + "-" + suffix
        batch = call("POST", "/batches", {"batch_no": no, "documents": [{"document_no": str(i + 1), "payload": {"mode": mode}} for i, mode in enumerate(modes)]}, expected=201)
        return no, batch["documents"]

    def status(no):
        return call("GET", "/batches/" + no)

    def await_status(no, predicate):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            result = status(no)
            if predicate(result):
                return result
            time.sleep(0.05)
        raise AssertionError(("status never converged", result))

    def wait_retry(no):
        documents = call("GET", "/batches/" + no + "/documents")
        deadlines = [datetime.fromisoformat(d["next_retry_at"].replace("Z", "+00:00"))
                     for d in documents if d.get("next_retry_at")]
        assert deadlines, ("no retry scheduled", documents)
        delay = (max(deadlines) - datetime.now(timezone.utc)).total_seconds()
        assert delay <= 4.1, ("retry exceeds backoff window", delay)
        time.sleep(max(0, delay) + 0.05)

    call("GET", "/healthz", token="")
    call("GET", "/batches/unauthorized", token="wrong", expected=401)
    no, documents = create("mixed", ["manual", "success", "fail_once", "reject"])
    assert status(no)["status"] == "PENDING"
    print("1. CreateBatch: four documents persisted as PENDING.")
    call("POST", "/batches/" + no + "/process", expected=502)
    await_status(no, lambda s: s["counts"]["SUCCESS"] == 1 and s["counts"]["FAILED"] == 2)
    print("2. ProcessBatch: accepted, callback SUCCESS, and transport rejection are visible.")

    manual_id = documents[0]["id"]
    callback = {"callback_id": "manual-" + suffix, "document_id": manual_id, "attempt_count": 1, "status": "SUCCESS"}
    call("POST", "/callbacks", callback, token=api_token, expected=401)
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        list(pool.map(lambda _: call("POST", "/callbacks", callback, token=cb_token), range(24)))
    call("POST", "/callbacks", callback, token=cb_token)
    events = call("GET", "/documents/" + manual_id + "/callbacks")
    assert len(events) == 1, events
    collision = dict(callback, status="FAILED")
    call("POST", "/callbacks", collision, token=cb_token, expected=409)
    late = dict(collision, callback_id="late-" + suffix)
    call("POST", "/callbacks", late, token=cb_token)
    assert status(no)["counts"]["SUCCESS"] == 2
    print("3. Parallel duplicate callbacks create one record; late FAILED preserves SUCCESS.")

    wait_retry(no)
    assert call("POST", "/batches/" + no + "/retry")["retried"] == 2
    call("POST", "/batches/" + no + "/process", expected=502)
    await_status(no, lambda s: s["counts"]["SUCCESS"] == 3)
    wait_retry(no)
    assert call("POST", "/batches/" + no + "/retry")["retried"] == 1
    call("POST", "/batches/" + no + "/process", expected=502)
    assert call("POST", "/batches/" + no + "/retry")["retried"] == 0
    call("POST", "/batches/" + no + "/process")
    final = status(no)
    ds = call("GET", "/batches/" + no + "/documents")
    assert [d["attempt_count"] for d in ds] == [1, 1, 2, 3], ds
    assert final["done"] and final["counts"]["SUCCESS"] == 3 and final["counts"]["FAILED"] == 1
    print("4. Retry stops at attempt 3; COMPLETED can include exhausted FAILED.")

    batches = [create("parallel-a", ["success"] * 12)[0], create("parallel-b", ["success"] * 12)[0]]
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        list(pool.map(lambda batch: call("POST", "/batches/" + batch + "/process"), batches))
    for batch in batches:
        await_status(batch, lambda s: s["counts"]["SUCCESS"] == 12)
    stats = call("GET", "/stats", token=principal_token, base=args.principal_url)
    assert stats["max_inflight"] == 3, stats
    print("5. Two concurrent batches (24 documents): global max_inflight = 3.")

    timeout_no, _ = create("timeout", ["timeout"])
    call("POST", "/batches/" + timeout_no + "/process", expected=504)
    assert status(timeout_no)["counts"]["FAILED"] == 1
    print("6. Submit timeout returns 504 and persists FAILED.")
    report = {"mixed_batch": no, "mixed_status": final, "documents": ds, "parallel_batches": batches, "principal_stats": stats, "timeout_batch": timeout_no, "result": "PASS"}
    Path("artifacts").mkdir(exist_ok=True)
    Path("artifacts/demo.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print("PASS. Evidence: artifacts/demo.json. Data remains in PostgreSQL.")


if __name__ == "__main__":
    main()
