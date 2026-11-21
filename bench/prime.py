"""Build the plugin's ledger for each task without running an agent.

The compromised-agent test (forced.py) needs a conversation the plugin has
read. Normally that comes from a run.py pass. This sends each task's turns
through the gateway once, so the plugin's PreLLMHook reads the user's messages
and builds the ledger; the model's reply is not used, and a provider error
after the hook ran is fine. Use it when no agent model is available (for
example, when its daily quota is spent).

It writes a results-like file that forced.py can read: the session of each
conversation, plus the requested actions an agent really sent in a reference
run (so the permitted actions are tested with real arguments).

Usage:
  python3 bench/prime.py --port 8100 --model gai/<model> \
      --reference results/without-<model>.jsonl --out runs/forced/sessions.jsonl
"""
import argparse
import json
import os
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
VK = "sk-bf-saidno-local"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--reference", required=True, help="a run.py results file with the agent's real requested calls")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    tasks = json.load(open(os.path.join(HERE, "tasks.json")))
    executed = {}
    for line in open(a.reference):
        r = json.loads(line)
        if r["run"] == 1 and r["task"] not in executed:
            executed[r["task"]] = r["executed"]
    stamp = time.strftime("%H%M%S")
    with open(a.out, "w") as out:
        for t in tasks:
            session = f"forced-{t['id']}-{stamp}"
            body = json.dumps({"model": a.model, "max_tokens": 1, "messages": t["turns"]}).encode()
            req = urllib.request.Request(
                f"http://127.0.0.1:{a.port}/v1/chat/completions", data=body, method="POST",
                headers={"content-type": "application/json", "x-bf-vk": VK, "x-said-no-session": session})
            try:
                with urllib.request.urlopen(req, timeout=120) as r:
                    r.read()
                status = "ok"
            except urllib.error.HTTPError as e:
                status = f"HTTP {e.code}"
            except Exception as e:  # the ledger is built before the provider is called
                status = repr(e)[:80]
            out.write(json.dumps({"task": t["id"], "run": 1, "session": session,
                                  "executed": executed.get(t["id"], []), "prime_status": status}) + "\n")
            print(f"{t['id']} {session} {status}")


if __name__ == "__main__":
    main()
