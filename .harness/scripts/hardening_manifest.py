#!/usr/bin/env python3
"""Hardening manifest tooling for the Harness hardened container pipeline.

Subcommands: validate, import, report, clamav-to-sarif.
"""

import argparse
import hashlib
import json
import os
import re
import shlex
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

import yaml

SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
DIGEST_RE = re.compile(r"@sha256:[0-9a-f]{64}$")
FILENAME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+-]*$")
REQUIRED_KEYS = ("version", "name", "labels", "maintainers")
ROOT_USERS = {"0", "root"}
CHUNK = 1024 * 1024
TIMEOUT_SECONDS = 60


def eprint(*args):
    print(*args, file=sys.stderr)


def load_manifest(path):
    with open(path, encoding="utf-8") as fh:
        data = yaml.safe_load(fh)
    if not isinstance(data, dict):
        raise ValueError(f"{path}: manifest must be a YAML mapping")
    return data


def validate_manifest(manifest):
    errors = []
    for key in REQUIRED_KEYS:
        if key not in manifest or manifest[key] in (None, "", [], {}):
            errors.append(f"missing required key '{key}'")

    if "version" in manifest and str(manifest["version"]) != "1":
        errors.append(f"unsupported manifest version {manifest['version']!r}; expected \"1\"")

    name = manifest.get("name")
    if name is not None and not (isinstance(name, str) and re.fullmatch(r"[a-z0-9][a-z0-9._/-]*", name)):
        errors.append(f"name {name!r} must be a lowercase repository path")

    labels = manifest.get("labels")
    if labels is not None:
        if not isinstance(labels, dict) or not all(isinstance(k, str) and isinstance(v, str) for k, v in labels.items()):
            errors.append("labels must be a mapping of strings")

    maintainers = manifest.get("maintainers")
    if maintainers is not None:
        if not isinstance(maintainers, list):
            errors.append("maintainers must be a list")
        else:
            for i, entry in enumerate(maintainers):
                if not isinstance(entry, dict) or not entry.get("name") or not entry.get("email"):
                    errors.append(f"maintainers[{i}] requires name and email")

    tags = manifest.get("tags", [])
    if not isinstance(tags, list) or not all(isinstance(t, str) and re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}", t) for t in tags):
        errors.append("tags must be a list of valid image tags")

    args = manifest.get("args", {})
    if args is not None and (not isinstance(args, dict) or not all(isinstance(v, (str, int, float)) for v in args.values())):
        errors.append("args must be a mapping of scalar values")

    errors.extend(validate_resources(manifest.get("resources")))
    errors.extend(validate_container(manifest.get("container")))
    return errors


def validate_resources(resources):
    if resources in (None, []):
        return []
    if not isinstance(resources, list):
        return ["resources must be a list"]
    errors = []
    seen = set()
    for i, res in enumerate(resources):
        where = f"resources[{i}]"
        if not isinstance(res, dict) or not isinstance(res.get("url"), str):
            errors.append(f"{where}: url is required")
            continue
        url = res["url"]
        scheme = urllib.parse.urlparse(url).scheme
        if scheme in ("http", "https"):
            filename = res.get("filename")
            if not isinstance(filename, str) or not FILENAME_RE.fullmatch(filename):
                errors.append(f"{where}: filename is required and must be a plain file name")
            elif filename in seen:
                errors.append(f"{where}: duplicate filename {filename}")
            else:
                seen.add(filename)
            validation = res.get("validation")
            if not isinstance(validation, dict):
                errors.append(f"{where}: validation is required for {scheme} resources")
            else:
                if validation.get("type") != "sha256":
                    errors.append(f"{where}: validation.type must be sha256")
                if not SHA256_RE.fullmatch(str(validation.get("value", ""))):
                    errors.append(f"{where}: validation.value must be 64 lowercase hex characters")
            auth = res.get("auth")
            if auth is not None and not (
                isinstance(auth, dict) and auth.get("type") == "basic" and re.fullmatch(r"[A-Za-z0-9_-]+", str(auth.get("id", "")))
            ):
                errors.append(f"{where}: auth must be {{type: basic, id: <id>}}")
        elif scheme == "docker":
            if not DIGEST_RE.search(url):
                errors.append(f"{where}: docker:// resources must be pinned with @sha256:<64 hex>")
        else:
            errors.append(f"{where}: unsupported url scheme {scheme!r}")
    return errors


def validate_container(container):
    if container is None:
        return []
    if not isinstance(container, dict):
        return ["container must be a mapping"]
    errors = []
    if "user" in container and str(container["user"]).strip().lower() in ROOT_USERS:
        errors.append("container.user must not be root")
    expose = container.get("expose", [])
    if not isinstance(expose, list) or not all(isinstance(p, int) and 0 < p < 65536 for p in expose):
        errors.append("container.expose must be a list of ports")
    return errors


def logical_lines(text):
    buf = ""
    for raw in text.splitlines():
        line = raw.strip()
        if not buf and (not line or line.startswith("#")):
            continue
        if line.startswith("#"):
            continue
        if line.endswith("\\"):
            buf += line[:-1] + " "
            continue
        yield buf + line
        buf = ""
    if buf:
        yield buf


def dockerfile_bases(path):
    text = Path(path).read_text(encoding="utf-8")
    arg_defaults = {}
    aliases = set()
    bases = []
    seen_from = False
    for line in logical_lines(text):
        parts = line.split()
        instruction = parts[0].upper()
        if instruction == "ARG" and not seen_from:
            for token in parts[1:]:
                key, _, value = token.partition("=")
                arg_defaults[key] = value
        if instruction != "FROM":
            continue
        seen_from = True
        tokens = [t for t in parts[1:] if not t.startswith("--")]
        if not tokens:
            continue
        image = tokens[0]
        alias = tokens[2].lower() if len(tokens) >= 3 and tokens[1].upper() == "AS" else None
        resolved = re.sub(r"\$\{(\w+)\}|\$(\w+)", lambda m: arg_defaults.get(m.group(1) or m.group(2), m.group(0)), image)
        if resolved.lower() not in aliases and resolved.lower() != "scratch":
            bases.append(resolved)
        if alias:
            aliases.add(alias)
    return bases


def check_pinning(bases):
    return [b for b in bases if "$" in b or not DIGEST_RE.search(b)]


def cmd_validate(args):
    try:
        manifest = load_manifest(args.manifest)
    except (OSError, ValueError, yaml.YAMLError) as exc:
        eprint(f"ERROR: {exc}")
        return 1

    errors = validate_manifest(manifest)
    warnings = []

    try:
        bases = dockerfile_bases(args.dockerfile)
    except OSError as exc:
        errors.append(f"cannot read Dockerfile: {exc}")
        bases = []
    if args.digest_pinning != "off":
        for base in check_pinning(bases):
            message = f"{args.dockerfile}: base image '{base}' is not pinned by digest"
            (errors if args.digest_pinning == "strict" else warnings).append(message)

    for w in warnings:
        eprint(f"WARNING: {w}")
    for e in errors:
        eprint(f"ERROR: {e}")
    if errors:
        eprint(f"{args.manifest}: {len(errors)} error(s), {len(warnings)} warning(s)")
        return 1

    eprint(f"{args.manifest}: valid ({len(warnings)} warning(s))")
    if args.emit_env:
        title = manifest["labels"].get("org.opencontainers.image.title") or manifest["name"]
        tags = " ".join(manifest.get("tags") or [])
        for key, value in (("IMAGE_NAME", manifest["name"]), ("IMAGE_TITLE", title), ("IMAGE_TAGS", tags)):
            print(f"export {key}={shlex.quote(value)}")
    return 0


class StripAuthOnCrossHostRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        new = super().redirect_request(req, fp, code, msg, headers, newurl)
        if new is not None and urllib.parse.urlparse(newurl).netloc != urllib.parse.urlparse(req.full_url).netloc:
            new.headers.pop("Authorization", None)
            new.unredirected_hdrs.pop("Authorization", None)
        return new


def basic_auth_header(auth_id):
    key = re.sub(r"[^A-Za-z0-9]", "_", auth_id).upper()
    user = os.environ.get(f"RESOURCE_AUTH_{key}_USERNAME")
    password = os.environ.get(f"RESOURCE_AUTH_{key}_PASSWORD")
    if user is None or password is None:
        raise RuntimeError(f"missing env RESOURCE_AUTH_{key}_USERNAME / RESOURCE_AUTH_{key}_PASSWORD")
    import base64

    return "Basic " + base64.b64encode(f"{user}:{password}".encode()).decode()


def download(res, dest):
    headers = {"User-Agent": "hardening-manifest-import"}
    if res.get("auth"):
        headers["Authorization"] = basic_auth_header(str(res["auth"]["id"]))
    opener = urllib.request.build_opener(StripAuthOnCrossHostRedirect)
    target = dest / res["filename"]
    digest = hashlib.sha256()
    try:
        with opener.open(urllib.request.Request(res["url"], headers=headers), timeout=TIMEOUT_SECONDS) as resp, open(target, "wb") as out:
            while chunk := resp.read(CHUNK):
                digest.update(chunk)
                out.write(chunk)
        expected = res["validation"]["value"]
        if digest.hexdigest() != expected:
            raise RuntimeError(f"sha256 mismatch: expected {expected}, got {digest.hexdigest()}")
    except BaseException:
        target.unlink(missing_ok=True)
        raise
    return target


def cmd_import(args):
    try:
        manifest = load_manifest(args.manifest)
    except (OSError, ValueError, yaml.YAMLError) as exc:
        eprint(f"ERROR: {exc}")
        return 1
    errors = validate_resources(manifest.get("resources"))
    if errors:
        for e in errors:
            eprint(f"ERROR: {e}")
        return 1

    dest = Path(args.dest)
    dest.mkdir(parents=True, exist_ok=True)
    failed = 0
    for res in manifest.get("resources") or []:
        scheme = urllib.parse.urlparse(res["url"]).scheme
        if scheme == "docker":
            print(f"docker resource {res['url']}: pull by digest at build time")
            continue
        try:
            target = download(res, dest)
            print(f"downloaded {res['url']} -> {target} (sha256 verified)")
        except (RuntimeError, OSError, urllib.error.URLError) as exc:
            eprint(f"ERROR: {res['url']}: {exc}")
            failed += 1
    return 1 if failed else 0


def sarif_summary(path):
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
        runs = data.get("runs") or []
        if data.get("version") and runs:
            return sum(len(r.get("results") or []) for r in runs)
    except (OSError, ValueError, AttributeError):
        pass
    return None


def cmd_report(args):
    try:
        manifest = load_manifest(args.manifest)
    except (OSError, ValueError, yaml.YAMLError) as exc:
        eprint(f"ERROR: {exc}")
        return 1

    labels = manifest.get("labels") or {}
    container = manifest.get("container") or {}
    print(f"# Findings summary: {manifest.get('name')}\n")
    print("## Manifest\n")
    print("| Field | Value |\n| --- | --- |")
    print(f"| Name | {manifest.get('name')} |")
    print(f"| Title | {labels.get('org.opencontainers.image.title', '')} |")
    print(f"| Tags | {', '.join(manifest.get('tags') or [])} |")
    print(f"| User | {container.get('user', '')} |")
    print(f"| Exposed ports | {', '.join(str(p) for p in container.get('expose') or [])} |")
    print(f"| Resources | {len(manifest.get('resources') or [])} |")
    print("\n## Scan results\n")

    results = Path(args.results)
    files = sorted(p for p in results.rglob("*") if p.is_file()) if results.is_dir() else []
    if not files:
        print(f"No result files found in {results}.")
        return 0
    print("| File | Size (bytes) | Findings |\n| --- | --- | --- |")
    for path in files:
        count = sarif_summary(path) if path.suffix in (".sarif", ".json") else None
        print(f"| {path.relative_to(results).as_posix()} | {path.stat().st_size} | {'n/a' if count is None else count} |")
    return 0


FOUND_RE = re.compile(r"^(?P<path>.+): (?P<sig>\S+) FOUND$")


def cmd_clamav_to_sarif(args):
    try:
        lines = Path(args.log).read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        eprint(f"ERROR: {exc}")
        return 1

    prefix = args.strip_prefix.rstrip("/") + "/" if args.strip_prefix else ""
    rules = {}
    results = []
    for line in lines:
        match = FOUND_RE.match(line.strip())
        if not match:
            continue
        sig = match["sig"]
        path = match["path"]
        if prefix and path.startswith(prefix):
            path = path[len(prefix):]
        rules.setdefault(sig, {"id": sig, "name": sig, "shortDescription": {"text": f"ClamAV signature {sig}"}})
        results.append(
            {
                "ruleId": sig,
                "level": "error",
                "message": {"text": f"ClamAV detected {sig} in {path}"},
                "locations": [{"physicalLocation": {"artifactLocation": {"uri": path}}}],
            }
        )

    sarif = {
        "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
        "version": "2.1.0",
        "runs": [
            {
                "tool": {"driver": {"name": "ClamAV", "informationUri": "https://www.clamav.net", "rules": list(rules.values())}},
                "results": results,
            }
        ],
    }
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(sarif, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {out}: {len(results)} finding(s)")
    return 0


def build_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("validate", help="validate a hardening manifest and its Dockerfile")
    p.add_argument("--manifest", required=True)
    p.add_argument("--dockerfile", required=True)
    p.add_argument("--digest-pinning", choices=("off", "warn", "strict"), default="warn")
    p.add_argument("--emit-env", action="store_true", help="print shell export lines on stdout")
    p.set_defaults(func=cmd_validate)

    p = sub.add_parser("import", help="download checksum-verified manifest resources")
    p.add_argument("--manifest", required=True)
    p.add_argument("--dest", required=True)
    p.set_defaults(func=cmd_import)

    p = sub.add_parser("report", help="print a markdown findings summary")
    p.add_argument("--results", required=True)
    p.add_argument("--manifest", required=True)
    p.set_defaults(func=cmd_report)

    p = sub.add_parser("clamav-to-sarif", help="convert clamscan output to SARIF 2.1.0")
    p.add_argument("--log", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--strip-prefix", default="")
    p.set_defaults(func=cmd_clamav_to_sarif)
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
