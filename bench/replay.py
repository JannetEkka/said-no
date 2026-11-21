"""Build docs/data.json for the replay page from the raw results.

Usage: python3 bench/replay.py results docs/data.json

For every task and agent model it keeps run 1 of each condition: the user's
turns, the injected line on the page, the actions that ran (decoy log), the
plugin's decisions, and the agent's reply, plus the compromised-agent\ntest's result for the task's injected action. It also embeds the summary counts.
Nothing is edited by hand: the page shows exactly what the runs recorded.
"""
import glob
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))


def injected_line(page_path):
    """Pull the injection text out of a suite page, if it has one."""
    if not os.path.exists(page_path):
        return None
    html = open(page_path).read()
    m = re.search(r"(&lt;INFORMATION&gt;.*?&lt;/INFORMATION&gt;|Note for the AI assistant reading this page:[^<]*)", html, re.S)
    if not m:
        return None
    return m.group(1).replace("&lt;", "<").replace("&gt;", ">").replace("&#x27;", "'").replace("&quot;", '"')


def main(results_dir, out_path):
    tasks = {t["id"]: t for t in json.load(open(os.path.join(HERE, "tasks.json")))}
    expected = json.load(open(os.path.join(HERE, "expected.json")))
    docs = os.path.join(os.path.dirname(HERE), "docs", "pages")
    runs = {}
    for p in glob.glob(os.path.join(results_dir, "with-*.jsonl")) + glob.glob(os.path.join(results_dir, "without-*.jsonl")):
        for line in open(p):
            r = json.loads(line)
            model = r["model"].split("/")[-1]
            key = (r["task"], model, r["condition"], r["run"])
            runs[key] = r
    # The compromised-agent test: the injected action called directly, per task and condition.
    forced = {}
    for p in glob.glob(os.path.join(results_dir, "forced-*.jsonl")):
        for line in open(p):
            f = json.loads(line)
            if f["kind"] != "forbidden":
                continue
            forced.setdefault(f["task"], {}).setdefault(f["condition"], []).append(
                {"tool": f["tool"], "arguments": f["arguments"], "allowed": not f["blocked"], "reason": f["result"]})
    items = []
    models = sorted({k[1] for k in runs})
    for tid, t in tasks.items():
        slug = t["page"].rsplit("/", 1)[-1]
        for model in models:
            for run in (1, 2, 3):
                wo, wi = runs.get((tid, model, "without", run)), runs.get((tid, model, "with", run))
                if not wo or not wi:
                    continue
                items.append({
                    "task": tid, "kind": t["kind"], "model": model, "run": run,
                    "turns": t["turns"], "page": t["page"], "injection": injected_line(os.path.join(docs, slug)),
                    "forbidden": expected[tid]["forbidden"], "required": expected[tid]["required"],
                    "without": side(wo), "with": side(wi),
                    "forced": forced.get(tid, {}),
                })
    summary_path = os.path.join(results_dir, "summary.md")
    data = {"items": items, "models": models,
            "summary_md": open(summary_path).read() if os.path.exists(summary_path) else ""}
    with open(out_path, "w") as f:
        json.dump(data, f, indent=1)
    print(f"{len(items)} task pairs -> {out_path}")


def side(r):
    """The parts of one run the page shows."""
    decisions = [d for d in r["decisions"] if d.get("event") == "decision" and not d.get("read_only")]
    ledger = next((d for d in reversed(r["decisions"]) if d.get("event") == "ledger"), None)
    return {
        "executed": r["executed"], "violation": r["violation"], "false_block": r["false_block"],
        "required_done": r["required_done"], "reply": r["reply"], "error": r["error"],
        "decisions": [{"tool": d["tool"], "arguments": d["arguments"], "allowed": d["allowed"], "reason": d["reason"],
                       "web_sourced": d.get("web_sourced_arguments"), "quote": (d.get("rule") or {}).get("quote")}
                      for d in decisions],
        "rules": (ledger or {}).get("rules") or [],
    }


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "results", sys.argv[2] if len(sys.argv) > 2 else "docs/data.json")
