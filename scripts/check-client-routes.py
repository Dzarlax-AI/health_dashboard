#!/usr/bin/env python3
"""Compare registered routes, OpenAPI and client call sites. Run from any directory."""
import argparse
import csv
import io
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
INVENTORY = ROOT / "contracts/route-inventory.tsv"
REGISTER = re.compile(r'\bmux\.Handle(?:Func)?\(\s*"([^"]+)"\s*,\s*([^\n]+)')
# Go handlers without a method-qualified mux pattern historically check r.Method
# inside the handler. Report supported operations (implicit HEAD is omitted).
HANDLER = re.compile(r"^func \(h \*Handler\) (\w+)\([^\n]*\{(.*?)^}", re.M | re.S)
HTTP_METHOD = re.compile(r"http\.Method(Get|Post|Put|Patch|Delete)")


def implicit_methods(body):
    methods = {method.upper() for method in HTTP_METHOD.findall(body)}
    if not methods:
        return "GET"
    if methods == {"POST"} and "r.Method == http.MethodPost" in body:
        methods.add("GET")
    return ",".join(sorted(methods))


API_LITERAL = re.compile(r'''["'`](/(?:api/|fragments/|health(?=["'`/]))[a-zA-Z0-9_{}/-]*)''')
PROTOCOLS = {
    "/health": ("GET,POST", "ingest-key"),
    "/health/hourly": ("GET,POST", "ingest-key"),
    "/health/vitals": ("GET,POST", "ingest-key"),
    "/health/workouts": ("GET,POST", "ingest-key"),
    "/health/checkpoint": ("GET", "profile"),
    "/health/sleep/v2": ("POST", "ingest-key"),
    "/healthz": ("GET", "probe"),
    "/readyz": ("GET", "probe"),
    "/mcp": ("MCP", "mcp-auth"),
    "/mcp/": ("MCP", "mcp-auth"),
    "/mcp/oauth": ("MCP", "access-jwt"),
    "/mcp/oauth/": ("DENY", "none"),
    "/api/telegram/webhook/": ("POST", "telegram-webhook"),
}


def files(directory, suffixes):
    return sorted(p for p in directory.rglob("*") if p.suffix in suffixes
                  and not p.name.endswith("_test.go") and ".test." not in p.name
                  and "generated" not in p.parts and "__tests__" not in p.parts)


def registered():
    rows = set()
    handlers = {}
    for file in files(ROOT / "internal/ui", {".go"}):
        handlers.update(HANDLER.findall(file.read_text()))
    for directory in ("internal", "cmd/server"):
        for file in files(ROOT / directory, {".go"}):
            source = file.read_text()
            for pattern, target in REGISTER.findall(source):
                path = pattern.split()[-1]
                if path in PROTOCOLS:
                    methods, access = PROTOCOLS[path]
                    kind = "protocol"
                elif path.startswith(("/api/", "/fragments/")):
                    match = re.match(r"h\.(adminGuard|guard)\(h\.(\w+)\)", target)
                    if not match:
                        raise ValueError(f"Classify {path} in {file.name}")
                    guard, handler = match.groups()
                    access = "admin" if guard == "adminGuard" else "profile"
                    kind = "compat" if path.startswith("/fragments/") else "api"
                    methods = pattern.partition(" ")[0] if " " in pattern else implicit_methods(handlers[handler])
                else:
                    if path.startswith(("/health/", "/mcp/")):
                        raise ValueError(f"Classify protocol route {path} in {file.name}")
                    continue
                for method in methods.split(","):
                    rows.add((path, method, access, kind))
            if "mcpserver" in file.parts:
                for tool in re.findall(r's\.AddTool\(mcp\.NewTool\("([^"]+)"', source):
                    rows.add((tool, "tools/call", "mcp-auth", "mcp-tool"))
            if "mux.Handle(oauth.MetadataPath(cfg.Resource)," in source:
                rows.add(("/.well-known/oauth-protected-resource/mcp", "GET", "public", "protocol"))
    return rows


def consumer_paths(sources):
    found = set()
    for file in sources:
        source = file.read_text()
        for line in source.splitlines():
            if line.lstrip().startswith(("//", "/*", "*", "<!--")):
                continue
            for path in API_LITERAL.findall(line):
                # Path parameters have different interpolation syntax in each client.
                if path.endswith("/"):
                    if path == "/api/section/" and ('\\(key)' in line or '${key}' in line):
                        path += "{key}"
                    elif path == "/api/admin/users/" and "encodeURIComponent(username)" in line:
                        path += "{username}/api-key"
                    elif path == "/api/inbox/" and ('\\(' in line or '${' in line):
                        path += "{id}"
                    elif path == "/api/prompts/" and ('\\(' in line or '${' in line) and "/answer" in line:
                        path += "{prompt_id}/answer"
                    elif path == "/api/push/devices/" and ('\\(' in line or '${' in line):
                        path += "{device_id}"
                if path.endswith("/admin-readiness-onboarding/") and "+ step" in line:
                    found.update(path + step for step in re.findall(r"onboardingRefreshStep\('(step-[0-9-]+(?:plan)?)'\)", source))
                    continue
                found.add(path)
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ios", type=Path, help="Health Sync checkout for live native source checks")
    parser.add_argument("--write", action="store_true", help="regenerate after reviewing route changes")
    args = parser.parse_args()
    rows = registered()
    paths = {row[0] for row in rows}
    web = ROOT / "apps/web/src"
    react_files = files(web, {".tsx", ".ts"})
    react = consumer_paths(react_files)
    legacy = consumer_paths(files(ROOT / "internal/ui/templates", {".html"}) +
                            files(ROOT / "internal/ui/static", {".js"}))
    native = set()
    if args.ios:
        directory = args.ios / "health-sync"
        if not (directory / "ServerClient.swift").is_file():
            parser.error("--ios must point at a Health Sync checkout")
        native = consumer_paths(files(directory, {".swift"}))
    elif INVENTORY.exists():
        with INVENTORY.open() as f:
            native = {row["path"] for row in csv.DictReader(f, delimiter="\t") if "ios" in row["path_clients"].split(",")}
    if args.write and not args.ios:
        parser.error("--write requires --ios")
    contract = json.loads((ROOT / "contracts/openapi.json").read_text())["paths"]
    errors = []
    for file in react_files:
        if not file.is_relative_to(web / "api") and consumer_paths([file]):
            errors.append(f"Call the shared API client from {file.relative_to(ROOT)}")
    for client, used in (("React", react), ("iOS", native), ("Go templates", legacy)):
        for path in sorted(used - paths):
            errors.append(f"{client}: missing registration for {path}")
    for path in sorted(react | native):
        if path not in PROTOCOLS and path not in contract:
            errors.append(f"Client bypasses OpenAPI: {path}")
    expected = {(p, m.lower()) for p, m, _, _ in rows}
    ids = {}
    for path, operations in contract.items():
        for method in operations.keys() & {"get", "post", "put", "patch", "delete"}:
            if (path, method) not in expected:
                errors.append(f"Unregistered contract operation: {method.upper()} {path}")
            identity = operations[method].get("operationId")
            if not identity or identity in ids:
                errors.append(f"Missing or reused operationId at {method.upper()} {path}: {identity}")
            ids[identity] = path
    output = io.StringIO()
    writer = csv.writer(output, delimiter="\t", lineterminator="\n")
    writer.writerow(("path", "method", "access", "kind", "openapi", "path_clients"))
    for path, method, access, kind in sorted(rows):
        callers = [name for name, used in (("react", react), ("ios", native), ("go-web", legacy)) if path in used]
        covered = method.lower() in contract.get(path, {})
        if covered:
            role = contract[path][method.lower()].get("x-required-role", "profile")
            if role != access:
                errors.append(f"Access mismatch: {method} {path}: route={access}, contract={role}")
        if kind == "api" and not covered:
            errors.append(f"Undocumented API operation: {method} {path}")
        writer.writerow((path, method, access, kind, "yes" if covered else "-", ",".join(callers)))
    if args.write and not errors:
        INVENTORY.write_text(output.getvalue())
    elif not args.write and (not INVENTORY.exists() or output.getvalue() != INVENTORY.read_text()):
        errors.append("Route inventory drift. Inspect registrations and clients, then regenerate with --ios and --write.")
    for error in errors:
        print(error, file=sys.stderr)
    print(f"{len(rows)} registered operations, {len(react)} React paths and {len(native)} iOS paths (including ingest)")
    return bool(errors)


if __name__ == "__main__":
    sys.exit(main())
