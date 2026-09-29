#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation. All rights reserved.
# Licensed under the MIT License.

"""Verify the pinned executor, then forward one externally approved service plan."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


HERE = Path(__file__).resolve().parent
MODE = "existing-agent-cli-evaluation"


class PrerequisiteError(ValueError):
    pass


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise PrerequisiteError("JSON input contains duplicate keys")
        result[key] = value
    return result


def read_json(path):
    try:
        return json.loads(path.read_bytes(), object_pairs_hook=unique_object)
    except (OSError, ValueError, RecursionError):
        raise PrerequisiteError("A required JSON file is missing or invalid") from None


def verified_entrypoint(root):
    dependency = read_json(HERE / "dependency.json")
    root = root.resolve()
    for relative, expected in dependency["files"].items():
        path = (root / relative).resolve()
        if not path.is_relative_to(root):
            raise PrerequisiteError("Executor dependency escapes its checkout")
        try:
            actual = hashlib.sha256(path.read_bytes()).hexdigest()
        except OSError:
            raise PrerequisiteError("Stage the exact executor dependency before running this sample") from None
        if actual != expected:
            raise PrerequisiteError("Executor bytes differ from dependency.json; use the reviewed revision")
    return root / dependency["entrypoint"]


def check_plan(path, env):
    plan = read_json(path)
    if not isinstance(plan, dict) or plan.get("mode") != MODE:
        raise PrerequisiteError("This sample accepts only existing-agent-cli-evaluation plans")
    for key in ("AZD_SCENARIO_LIVE_APPROVAL_SHA256", "AZD_SCENARIO_LIVE_AUTH_CONFIG"):
        if not env.get(key):
            raise PrerequisiteError("Protected plan approval and isolated CI authentication are required")
    # The executor, not this adapter, validates approval, native identity, bytes and resource scope.


def blocked_receipt(output, message):
    output.mkdir(parents=True)
    (output / "service-status.json").write_text(json.dumps({
        "status": "BLOCKED",
        "execution": "NOT RUN",
        "error": {"type": "SamplePrerequisite", "message": message},
        "cleanup": {"status": "NOT RUN"},
    }, indent=2) + "\n", encoding="utf-8")


def execute(root, plan, output):
    if output.exists():
        print("BLOCKED: output directory must be new; existing evidence was not changed", file=sys.stderr)
        return 3
    try:
        entrypoint = verified_entrypoint(root)
        check_plan(plan, os.environ)
    except PrerequisiteError as error:
        blocked_receipt(output, str(error))
        print(f"BLOCKED: {error}", file=sys.stderr)
        return 3
    # No shell, plan rewriting, authentication, installer, approval bypass or second lifecycle.
    try:
        return subprocess.run(
            [sys.executable, "-B", "-E", "-s", str(entrypoint), "--plan", str(plan.resolve()),
             "--output", str(output.resolve())],
            stdin=subprocess.DEVNULL,
            check=False,
        ).returncode
    except OSError:
        blocked_receipt(output, "Could not start the verified executor")
        print("BLOCKED: could not start the verified executor", file=sys.stderr)
        return 3


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--executor-root", required=True, type=Path)
    parser.add_argument("--plan", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    return execute(args.executor_root, args.plan, args.output)


if __name__ == "__main__":
    sys.exit(main())
