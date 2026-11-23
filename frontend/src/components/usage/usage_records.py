import hashlib
import json
import os
import sqlite3
import time
from contextlib import closing
from datetime import datetime, timezone
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import HTTPRedirectHandler, Request, build_opener

REDIRECT_ERROR = "请把 SUB2API_USAGE_URL 配成最终的 HTTPS 地址"


class RejectRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        fp.close()
        raise SystemExit(REDIRECT_ERROR)


def get_page(opener, endpoint, key, params):
    request = Request(endpoint + "?" + urlencode(params),
                      headers={"Authorization": "Bearer " + key})
    while True:
        try:
            with opener.open(request, timeout=30) as response:
                return json.load(response)
        except HTTPError as exc:
            with exc:
                if 300 <= exc.code < 400:
                    raise SystemExit(REDIRECT_ERROR) from exc
                if exc.code != 429:
                    raise
                wait = int(exc.headers.get("Retry-After", "60"))
            time.sleep(wait)


def import_records(db, endpoint, key, end):
    hour = 3600
    overlap = int(os.environ.get("SUB2API_USAGE_OVERLAP_HOURS", "2")) * hour
    if overlap < 0:
        raise ValueError("SUB2API_USAGE_OVERLAP_HOURS must be nonnegative")
    scope = (endpoint, hashlib.sha256(key.encode()).hexdigest())
    opener = build_opener(RejectRedirects())
    db.execute("""CREATE TABLE IF NOT EXISTS usage_records (
        endpoint TEXT, id INTEGER, record TEXT NOT NULL, PRIMARY KEY (endpoint, id)
    )""")
    db.execute("""CREATE TABLE IF NOT EXISTS usage_imports (
        endpoint TEXT, key_fingerprint TEXT, first_start INTEGER NOT NULL,
        complete_until INTEGER, PRIMARY KEY (endpoint, key_fingerprint)
    )""")
    with db:
        # Keep the first window recoverable even if the very first run fails.
        first_start = int(os.environ.get("SUB2API_USAGE_START", end - hour))
        db.execute("INSERT OR IGNORE INTO usage_imports VALUES (?, ?, ?, NULL)",
                   (*scope, first_start))
        first_start, complete = db.execute(
            "SELECT first_start, complete_until FROM usage_imports WHERE endpoint=? AND key_fingerprint=?",
            scope).fetchone()
    start = first_start if complete is None else complete - overlap
    while start < end:
        stop = min(start + 7 * 24 * hour, end)
        params = {"start_time": start, "end_time": stop, "limit": 500}
        while True:
            page = get_page(opener, endpoint, key, params)
            with db:
                db.executemany("INSERT OR IGNORE INTO usage_records VALUES (?, ?, ?)",
                               [(endpoint, row["id"], json.dumps(row)) for row in page["data"]])
                if not page["has_more"]:
                    # Commit the last page and watermark together. Overlapping jobs
                    # may finish out of order, so only advance a previous watermark.
                    db.execute("""UPDATE usage_imports SET complete_until=?
                        WHERE endpoint=? AND key_fingerprint=?
                        AND (complete_until IS NULL OR complete_until < ?)""", (stop, *scope, stop))
                    break
            params["cursor"] = page["next_cursor"]
            if not params["cursor"]:
                raise ValueError("Missing next_cursor on a non-final page")
        start = stop


if __name__ == "__main__":
    endpoint = os.environ["SUB2API_USAGE_URL"]
    key = os.environ["SUB2API_KEY"]
    end = datetime.now(timezone.utc).replace(minute=0, second=0, microsecond=0)
    with closing(sqlite3.connect(os.environ.get("SUB2API_USAGE_DB", "usage-records.sqlite3"))) as db:
        import_records(db, endpoint, key, int(end.timestamp()))
