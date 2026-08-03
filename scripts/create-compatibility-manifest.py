#!/usr/bin/env python3

"""Validate image-pair metadata and emit a compatibility manifest."""

import argparse
import json
import re
import sys


DIGEST_PATTERN = re.compile(r"sha256:[0-9a-f]{64}\Z")


def nonempty(value: str, label: str) -> str:
    if not value.strip():
        raise ValueError(f"{label} must be non-empty")
    return value


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Create a validated backend/frontend compatibility manifest."
    )
    parser.add_argument("--backend-image", required=True)
    parser.add_argument("--frontend-image", required=True)
    parser.add_argument("--backend-digest", required=True)
    parser.add_argument("--frontend-digest", required=True)
    parser.add_argument("--backend-revision", required=True)
    parser.add_argument("--frontend-revision", required=True)
    parser.add_argument("--backend-contract-version", required=True)
    parser.add_argument("--frontend-contract-version", required=True)
    parser.add_argument("--created-at", required=True)
    return parser.parse_args()


def create_manifest(args: argparse.Namespace) -> dict[str, object]:
    backend_image = nonempty(args.backend_image, "backend image")
    frontend_image = nonempty(args.frontend_image, "frontend image")
    backend_revision = nonempty(args.backend_revision, "backend revision")
    frontend_revision = nonempty(args.frontend_revision, "frontend revision")
    backend_contract = nonempty(
        args.backend_contract_version, "backend contract version"
    )
    frontend_contract = nonempty(
        args.frontend_contract_version, "frontend contract version"
    )
    created_at = nonempty(args.created_at, "created-at")

    if backend_revision != frontend_revision:
        raise ValueError("backend and frontend revisions must match")
    if backend_contract != frontend_contract:
        raise ValueError("backend and frontend contract versions must match")
    if not DIGEST_PATTERN.fullmatch(args.backend_digest):
        raise ValueError("backend digest must be sha256: followed by 64 lowercase hex chars")
    if not DIGEST_PATTERN.fullmatch(args.frontend_digest):
        raise ValueError(
            "frontend digest must be sha256: followed by 64 lowercase hex chars"
        )

    return {
        "schema_version": 1,
        "revision": backend_revision,
        "api_contract_version": backend_contract,
        "created_at": created_at,
        "backend": {
            "image": backend_image,
            "digest": args.backend_digest,
            "role": "backend",
        },
        "frontend": {
            "image": frontend_image,
            "digest": args.frontend_digest,
            "role": "frontend",
        },
    }


def main() -> int:
    args = parse_args()
    try:
        manifest = create_manifest(args)
    except ValueError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1

    json.dump(manifest, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
