"""Validates a snapshot against the dataset schemas from spec 0003, CFC-3.

Checks:
- manifest.json lists every dataset file, and its records and sha256 match the file;
- every record matches its dataset schema: required fields, types, formats, no unknown fields;
- no duplicate keys (`id`, or the natural key for datasets without `id`);
- every name in `sources` is a provider from data/sources.yaml.

Fields marked `?` in CFC-3 are optional: they may be absent or null.

Usage: python3 data/validate.py [snapshot_dir]   (default: data/snapshot)
Exits with 1 and prints file:line: field: problem for every violation.
"""

import hashlib
import json
import re
import sys
from pathlib import Path

DATA_DIR = Path(__file__).resolve().parent
SOURCES_FILE = DATA_DIR / "sources.yaml"

MAX_PRINTED_ERRORS = 200

# --- field checks: each returns an error message or None ---------------------

ISO2 = re.compile(r"^[A-Z]{2}$")
IATA = re.compile(r"^[A-Z]{3}$")
HHMM = re.compile(r"^([01]\d|2[0-3]):[0-5]\d$")
IANA_TZ = re.compile(r"^(UTC|[A-Za-z_]+(/[A-Za-z0-9_+\-]+)+)$")

VISA_STATUSES = {
    "home", "visa_free", "visa_on_arrival", "eta", "e_visa", "visa_required", "no_admission",
}
DESTINATION_TAGS = {"sea", "warm", "mountains", "city", "nature", "kids", "ski"}


def is_int(v):
    return isinstance(v, int) and not isinstance(v, bool)


def is_number(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def string(v):
    if not isinstance(v, str) or not v.strip():
        return "expected a non-empty string"


def string_or_empty(v):
    if not isinstance(v, str):
        return "expected a string"


def pattern(regex, what):
    def check(v):
        if not isinstance(v, str) or not regex.match(v):
            return f"expected {what}, got {v!r}"
    return check


country = pattern(ISO2, "ISO 3166-1 alpha-2 code in upper case")
iata = pattern(IATA, "IATA code (3 upper-case letters)")
hhmm = pattern(HHMM, "time HH:MM")
tz = pattern(IANA_TZ, "IANA time zone like Europe/Moscow")


def boolean(v):
    if not isinstance(v, bool):
        return "expected true or false"


def integer(min_value=None, max_value=None):
    def check(v):
        if not is_int(v):
            return f"expected an integer, got {v!r}"
        if min_value is not None and v < min_value:
            return f"expected >= {min_value}, got {v}"
        if max_value is not None and v > max_value:
            return f"expected <= {max_value}, got {v}"
    return check


def number(min_value=None, max_value=None):
    def check(v):
        if not is_number(v):
            return f"expected a number, got {v!r}"
        if min_value is not None and v < min_value:
            return f"expected >= {min_value}, got {v}"
        if max_value is not None and v > max_value:
            return f"expected <= {max_value}, got {v}"
    return check


lat = number(-90, 90)
lon = number(-180, 180)


def one_of(allowed):
    def check(v):
        if v not in allowed:
            return f"expected one of {sorted(allowed)}, got {v!r}"
    return check


def list_of(item_check, non_empty=False):
    def check(v):
        if not isinstance(v, list):
            return "expected a list"
        if non_empty and not v:
            return "expected a non-empty list"
        for i, item in enumerate(v):
            err = item_check(item)
            if err:
                return f"item {i}: {err}"
    return check


def string_or_int(v):
    if not (is_int(v) or (isinstance(v, str) and v.strip())):
        return "expected a non-empty string or an integer"


sources = list_of(string, non_empty=True)

# --- schemas -----------------------------------------------------------------
# fields: name -> (check, required). key: fields that must be unique together.

R, O = True, False

SCHEMAS = {
    "countries": {
        "key": ("code",),
        "fields": {"code": (country, R), "name_ru": (string, R), "name_en": (string, R)},
    },
    "visa": {
        "key": ("passport", "destination"),
        "fields": {
            "passport": (country, R),
            "destination": (country, R),
            "status": (one_of(VISA_STATUSES), R),
            "days": (integer(1), O),
            "sources": (sources, R),
        },
    },
    "cities": {
        "key": ("id",),
        "fields": {
            "id": (string, R),
            "name_ru": (string, R),
            "name_en": (string, R),
            "country_code": (country, R),
            "lat": (lat, R),
            "lon": (lon, R),
            "timezone": (tz, R),
            "population": (integer(0), R),
            "airports": (list_of(iata), R),
            "stations": (list_of(string), R),
        },
    },
    "airports": {
        "key": ("iata",),
        "fields": {
            "iata": (iata, R),
            "name": (string, R),
            "city_id": (string, R),
            "country_code": (country, R),
            "lat": (lat, R),
            "lon": (lon, R),
            "timezone": (tz, R),
            "scheduled": (boolean, R),
        },
    },
    "stations": {
        "key": ("code",),
        "fields": {
            "code": (string, R),
            "name": (string, R),
            "city_id": (string, R),
            "lat": (lat, R),
            "lon": (lon, R),
        },
    },
    "destinations": {
        "key": ("id",),
        "fields": {
            "id": (string, R),
            "city_id": (string, R),
            "name": (string, R),
            "country_code": (country, R),
            "tags": (list_of(one_of(DESTINATION_TAGS)), R),
            "airports": (list_of(iata), R),
            "stations": (list_of(string), R),
            "description": (string_or_empty, R),
            "sources": (sources, R),
        },
    },
    "climate": {
        "key": ("destination_id", "month"),
        "fields": {
            "destination_id": (string, R),
            "month": (integer(1, 12), R),
            "temp_max_c": (number(), R),
            "temp_min_c": (number(), R),
            "precipitation_mm": (number(0), R),
            "sea_temp_c": (number(), O),
        },
    },
    "hotels": {
        "key": ("id",),
        "fields": {
            "id": (string, R),
            "destination_id": (string, R),
            "name": (string, R),
            "lat": (lat, R),
            "lon": (lon, R),
            "stars": (integer(1, 5), O),
            "check_in_time": (hhmm, O),
            "check_out_time": (hhmm, O),
            "pets": (boolean, O),
            "osm_id": (string_or_int, R),
            "sources": (sources, R),
        },
    },
    "routes": {
        "key": ("from_iata", "to_iata"),
        "fields": {
            "from_iata": (iata, R),
            "to_iata": (iata, R),
            "distance_km": (number(0), R),
            "direct": (boolean, R),
        },
    },
    "rail_routes": {
        "key": ("from_station", "to_station"),
        "fields": {
            "from_station": (string, R),
            "to_station": (string, R),
            "duration_min": (integer(1), R),
            "departures": (list_of(hhmm), R),
            "train_numbers": (list_of(string), R),
        },
    },
    # One object; its inner structure is specified by P1-DS-07.
    "calibration": {"key": (), "fields": None, "single": True},
}


# --- validation ----------------------------------------------------------------


def load_source_names():
    # impulse: reads only `- provider:` lines, no YAML parser (stdlib has none);
    # switch to a YAML library if validate.py needs other fields of sources.yaml.
    if not SOURCES_FILE.exists():
        return set()
    names = set()
    for line in SOURCES_FILE.read_text(encoding="utf-8").splitlines():
        m = re.match(r"^-\s+provider:\s*(\S+)\s*$", line)
        if m:
            names.add(m.group(1).strip("\"'"))
    return names


def check_record(schema, record, known_sources):
    errors = []
    fields = schema["fields"]
    for name in record:
        if name not in fields:
            errors.append((name, "unknown field"))
    for name, (check, required) in fields.items():
        value = record.get(name)
        if value is None:
            if required:
                errors.append((name, "missing required field"))
            continue
        err = check(value)
        if err:
            errors.append((name, err))
        elif name == "sources":
            for src in value:
                if src not in known_sources:
                    errors.append((name, f"unknown provider {src!r}, not in data/sources.yaml"))
    return errors


def validate_dataset(path, name, known_sources):
    shown = path.as_posix()
    schema = SCHEMAS.get(name)
    if schema is None:
        return [f"{shown}: unknown dataset {name!r}, expected one of {sorted(SCHEMAS)}"]

    errors = []
    seen = {}
    records = 0
    try:
        lines = path.read_text(encoding="utf-8").split("\n")
    except UnicodeDecodeError as e:
        return [f"{shown}: not valid UTF-8: {e}"]

    for line_no, line in enumerate(lines, start=1):
        if not line.strip():
            continue
        records += 1
        try:
            record = json.loads(line)
        except json.JSONDecodeError as e:
            errors.append(f"{shown}:{line_no}: invalid JSON: {e.msg}")
            continue
        if not isinstance(record, dict):
            errors.append(f"{shown}:{line_no}: expected a JSON object")
            continue

        if schema["fields"] is not None:
            for field, problem in check_record(schema, record, known_sources):
                errors.append(f"{shown}:{line_no}: {field}: {problem}")

        if schema["key"]:
            key = tuple(json.dumps(record.get(k)) for k in schema["key"])
            if key in seen:
                key_text = ", ".join(f"{k}={record.get(k)!r}" for k in schema["key"])
                errors.append(f"{shown}:{line_no}: duplicate {key_text}, first seen at line {seen[key]}")
            else:
                seen[key] = line_no

    if schema.get("single") and records != 1:
        errors.append(f"{shown}: expected exactly one object, got {records}")
    return errors


def validate_manifest(snapshot_dir):
    manifest_path = snapshot_dir / "manifest.json"
    shown = manifest_path.as_posix()
    if not manifest_path.exists():
        return [f"{shown}: missing, run data/build.py"], []
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        entries = manifest["datasets"]
        manifest["built_at"]
    except (json.JSONDecodeError, KeyError, TypeError) as e:
        return [f"{shown}: malformed manifest: {e}"], []

    errors = []
    listed = []
    for entry in entries:
        try:
            name, file = entry["name"], entry["file"]
            records, sha256 = entry["records"], entry["sha256"]
            entry["schema_version"]
        except (KeyError, TypeError) as e:
            errors.append(f"{shown}: malformed dataset entry {entry!r}: missing {e}")
            continue
        path = snapshot_dir / file
        if not path.exists():
            errors.append(f"{shown}: dataset {name!r}: file {file} not found")
            continue
        content = path.read_bytes()
        if hashlib.sha256(content).hexdigest() != sha256:
            errors.append(f"{shown}: dataset {name!r}: sha256 does not match {file}, rebuild the snapshot")
        actual = sum(1 for line in content.decode("utf-8", "replace").split("\n") if line.strip())
        if actual != records:
            errors.append(f"{shown}: dataset {name!r}: records is {records}, file has {actual}")
        listed.append((path, name))

    listed_files = {p.name for p, _ in listed}
    for path in sorted(snapshot_dir.glob("*.jsonl")):
        if path.name not in listed_files:
            errors.append(f"{path.as_posix()}: not listed in manifest.json, rebuild the snapshot")
    return errors, listed


def main():
    snapshot_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else DATA_DIR / "snapshot"
    try:
        snapshot_dir = snapshot_dir.resolve().relative_to(Path.cwd())
    except ValueError:
        pass

    known_sources = load_source_names()
    errors, datasets = validate_manifest(snapshot_dir)
    for path, name in datasets:
        errors.extend(validate_dataset(path, name, known_sources))

    if errors:
        for e in errors[:MAX_PRINTED_ERRORS]:
            print(e, file=sys.stderr)
        if len(errors) > MAX_PRINTED_ERRORS:
            print(f"... and {len(errors) - MAX_PRINTED_ERRORS} more", file=sys.stderr)
        print(f"validation failed: {len(errors)} error(s)", file=sys.stderr)
        return 1
    print(f"snapshot ok: {len(datasets)} dataset(s) in {snapshot_dir.as_posix()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
