#!/usr/bin/env python3
"""Measures the knowledge search (KN-60): how often the passage that answers
each of a set of questions is among the 6 hits ask_knowledge gives, for
keyword, fuzzy and semantic search and the three ranked together. The
console asks each question of its own index (POST /api/v1/knowledge/evaluate,
an Admin's or a SuperAdmin's) and keeps nothing; this prints the report.

  CONSOLE=https://221b.example.com hack/knowledge-eval.py questions.jsonl [--json report.json]
  make knowledge-eval CONSOLE=https://221b.example.com QUESTIONS=questions.jsonl

Signing in, one of:
  CONSOLE_COOKIE  the value of the 221b_session cookie of a browser signed in
                  to the console (read from the environment, never printed);
  CONSOLE_USER    a local account, its password on stdin:
                  printf %s "$PW" | CONSOLE_USER=ann@example.com hack/knowledge-eval.py q.jsonl
CONSOLE_CA is a CA bundle for a console with a private certificate.

The questions: JSON Lines, one object a line,
  {"question": "How do I restore etcd?", "source": "Runbooks", "path": "etcd/restore.md", "heading": "From a snapshot"}
or tab-separated lines: question, source, path[, heading]. source is the
source's name or id ("wiki" for an approved page, path its page's path);
path is the item's path in its source or its address, or the end of either;
heading, optional, a long document's section (the end of its heading path).
Lines that start with # are left out.
"""
import http.cookiejar
import json
import os
import re
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request

MODES = [("keyword", "Keyword"), ("fuzzy", "Fuzzy"), ("semantic", "Semantic"), ("hybrid", "Hybrid")]


def questions(path):
    out = []
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            line = line.rstrip("\n")
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            if line.lstrip().startswith("{"):
                q = json.loads(line)
            else:
                cells = line.split("\t")
                if len(cells) < 3:
                    sys.exit("%s:%d: a line is question, source, path[, heading], apart by tabs" % (path, n))
                q = {"question": cells[0], "source": cells[1], "path": cells[2]}
                if len(cells) > 3 and cells[3]:
                    q["heading"] = cells[3]
            out.append(q)
    if not out:
        sys.exit("%s holds no question" % path)
    return out


class Console:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.jar = http.cookiejar.CookieJar()
        ctx = ssl.create_default_context(cafile=os.environ.get("CONSOLE_CA") or None)
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar), urllib.request.HTTPSHandler(context=ctx))
        # What a browser sends on the console's own requests.
        self.headers = {"Origin": self.base, "Sec-Fetch-Site": "same-origin", "Referer": self.base + "/"}
        cookie = os.environ.get("CONSOLE_COOKIE", "")
        if cookie:
            self.headers["Cookie"] = "221b_session=" + cookie.removeprefix("221b_session=")

    def req(self, method, path, body=None, form=False, timeout=600):
        data, h = None, dict(self.headers)
        if body is not None:
            if form:
                data = urllib.parse.urlencode(body).encode()
                h["Content-Type"] = "application/x-www-form-urlencoded"
            else:
                data = json.dumps(body).encode()
                h["Content-Type"] = "application/json"
        r = urllib.request.Request(self.base + path, data=data, headers=h, method=method)
        try:
            with self.op.open(r, timeout=timeout) as resp:
                return resp.status, resp.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    def signin(self, user, password):
        _, page = self.req("GET", "/signin")
        form = next((c.value for c in self.jar if c.name == "221b_form"), None)
        if not form:
            m = re.search(r'name="form" value="([^"]+)"', page)
            form = m.group(1) if m else ""
        self.req("POST", "/signin", {"email": user, "password": password, "form": form}, form=True)
        st, _ = self.req("GET", "/api/v1/me")
        if st != 200:
            sys.exit("signing in as %s failed (%s)" % (user, st))


def report(rep):
    n = rep["questions"]
    lines = ["# The knowledge search, measured", "",
             "%d questions; the passage that answers each among the first %d hits:" % (n, rep["limit"]), "",
             "| Search | Found | Share |", "| --- | --- | --- |"]
    for mode, label in MODES:
        lines.append("| %s | %d of %d | %.0f %% |" % (label, rep["hits"].get(mode, 0), n, 100 * rep["recall"].get(mode, 0)))
    if rep.get("note"):
        lines += ["", "Note: " + rep["note"] + "."]
    lines += ["", "| # | Question | " + " | ".join(label for _, label in MODES) + " |", "| --- | --- |" + " --- |" * len(MODES)]
    for i, a in enumerate(rep["answers"], 1):
        ranks = [str(a["ranks"].get(mode, 0) or "—") for mode, _ in MODES]
        lines.append("| %d | %s | %s |" % (i, a["question"].replace("|", "\\|"), " | ".join(ranks)))
    missed = [(i, a) for i, a in enumerate(rep["answers"], 1) if not a["ranks"].get("hybrid")]
    if missed:
        lines += ["", "## Not found by the hybrid search", ""]
        for i, a in missed:
            lines.append("%d. %s; it found: %s" % (i, a["question"], "; ".join(a.get("found") or ["nothing"])))
    return "\n".join(lines) + "\n"


def main():
    args = sys.argv[1:]
    out = None
    if "--json" in args:
        i = args.index("--json")
        out = args[i + 1]
        del args[i:i + 2]
    if len(args) != 1 or not os.environ.get("CONSOLE"):
        sys.exit(__doc__)
    qs = questions(args[0])
    c = Console(os.environ["CONSOLE"])
    if os.environ.get("CONSOLE_USER"):
        c.signin(os.environ["CONSOLE_USER"], sys.stdin.read().strip())
    elif not os.environ.get("CONSOLE_COOKIE"):
        sys.exit("sign in: CONSOLE_COOKIE, or CONSOLE_USER with the password on stdin")
    st, body = c.req("POST", "/api/v1/knowledge/evaluate", {"questions": qs})
    if st != 200:
        sys.exit("the console answered %s: %s" % (st, body[:500]))
    rep = json.loads(body)
    if out:
        with open(out, "w", encoding="utf-8") as f:
            json.dump(rep, f, indent=1)
    sys.stdout.write(report(rep))


if __name__ == "__main__":
    main()
