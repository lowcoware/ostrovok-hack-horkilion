"""Builds data/snapshot/ from loader outputs and manual overrides.

Input:  data/loaders/<source>/out/<dataset>.jsonl, written by the loaders.
        data/overrides/, manual corrections applied on top.
Output: data/snapshot/<dataset>.jsonl and data/snapshot/manifest.json.

The build is deterministic: the same input always gives byte-identical
dataset files and therefore the same sha256 in the manifest. Only
`built_at` changes between runs.

Usage: python3 data/build.py
"""

import hashlib
import json
import sys
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path

DATA_DIR = Path(__file__).resolve().parent
LOADERS_DIR = DATA_DIR / "loaders"
OVERRIDES_DIR = DATA_DIR / "overrides"
SNAPSHOT_DIR = DATA_DIR / "snapshot"

SCHEMA_VERSION = 1

# Dataset name -> function(records, overrides_dir) -> records.
# Each override is added by the subtask that introduces it (visa.csv: P1-DS-02.4).
OVERRIDES = {}


class BuildError(Exception):
    pass


def read_jsonl(path):
    shown = path.relative_to(DATA_DIR.parent).as_posix()
    records = []
    with path.open(encoding="utf-8") as f:
        for line_no, line in enumerate(f, start=1):
            if not line.strip():
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError as e:
                raise BuildError(f"{shown}:{line_no}: invalid JSON: {e.msg}") from None
            if not isinstance(obj, dict):
                raise BuildError(f"{shown}:{line_no}: expected a JSON object")
            records.append(obj)
    return records


def collect_loader_outputs():
    """Groups loader output files by dataset name; several loaders may feed one dataset."""
    datasets = defaultdict(list)
    for path in sorted(LOADERS_DIR.glob("*/out/*.jsonl")):
        datasets[path.stem].extend(read_jsonl(path))
    return datasets


def canonical_line(record):
    return json.dumps(record, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def write_dataset(name, records):
    lines = sorted(canonical_line(r) for r in records)
    content = "".join(line + "\n" for line in lines).encode("utf-8")
    (SNAPSHOT_DIR / f"{name}.jsonl").write_bytes(content)
    return {
        "name": name,
        "file": f"{name}.jsonl",
        "records": len(lines),
        "sha256": hashlib.sha256(content).hexdigest(),
        "schema_version": SCHEMA_VERSION,
    }


def build():
    datasets = collect_loader_outputs()
    for name, apply_override in OVERRIDES.items():
        datasets[name] = apply_override(datasets.get(name, []), OVERRIDES_DIR)

    SNAPSHOT_DIR.mkdir(exist_ok=True)
    # Drop datasets left over from earlier builds so the snapshot matches the manifest.
    for stale in SNAPSHOT_DIR.glob("*.jsonl"):
        if stale.stem not in datasets:
            stale.unlink()

    manifest = {
        "built_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "datasets": [write_dataset(name, datasets[name]) for name in sorted(datasets)],
    }
    manifest_text = json.dumps(manifest, ensure_ascii=False, indent=2) + "\n"
    (SNAPSHOT_DIR / "manifest.json").write_bytes(manifest_text.encode("utf-8"))
    return manifest


def main():
    try:
        manifest = build()
    except BuildError as e:
        print(f"build failed: {e}", file=sys.stderr)
        return 1
    print(f"snapshot built: {len(manifest['datasets'])} dataset(s)")
    for d in manifest["datasets"]:
        print(f"  {d['name']}: {d['records']} records, sha256 {d['sha256'][:12]}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
