# Copyright (c) Microsoft Corporation. All rights reserved.
# Licensed under the MIT License.
# cspell:ignore COLLECTIONURI COLLECTIONID DEFINITIONID JOBID JOBATTEMPT SOURCEVERSION TEAMPROJECT

from contextlib import redirect_stderr
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import run as sample


class SampleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        root = os.environ.get("SAMPLE_EXECUTOR_ROOT")
        if not root:
            raise RuntimeError("Set SAMPLE_EXECUTOR_ROOT to the exact local dependency export; see README.md")
        cls.root = Path(root).resolve()
        entrypoint = sample.verified_entrypoint(cls.root)
        sys.path.insert(0, str(entrypoint.parent))
        spec = importlib.util.spec_from_file_location("sample_service", entrypoint)
        cls.service = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.service)
        cls.addClassCleanup(sys.path.remove, str(entrypoint.parent))

    def fixture(self, provider="github"):
        plan = sample.read_json(sample.HERE / "plan.example.json")
        plan.update(
            provider=provider, workflowCommit="a" * 40, runId="42",
            expiresAt=(datetime.now(timezone.utc) + timedelta(hours=1)).isoformat(),
            approvalReference="mock-only", resourceOwner="mock-only",
            budgetControlReference="mock-only", budgetControlExternallyVerified=True, approvedBudget="1",
            clientId="00000000-0000-0000-0000-000000000001",
            tenantId="00000000-0000-0000-0000-000000000002",
            projectEndpoint="https://fixture.services.ai.azure.com/api/projects/fixture",
            agentName="fixture-agent", agentVersion="9",
            datasetFile=str(sample.HERE / "datasets" / "support.jsonl"),
            evaluator="builtin.task_adherence", judgeModel="fixture-judge",
            azdExecutable="mock-only-never-executed",
        )
        plan["datasetSha256"] = hashlib.sha256(Path(plan["datasetFile"]).read_bytes()).hexdigest()
        plan["versions"] = dict.fromkeys(plan["versions"], "mock-only")
        plan["binarySha256"] = dict.fromkeys(plan["binarySha256"], "b" * 64)
        if provider == "github":
            env = {
                "GITHUB_REPOSITORY": "fixture/repo", "GITHUB_REPOSITORY_ID": "123",
                "GITHUB_RUN_ID": "42", "GITHUB_SHA": "a" * 40,
                "GITHUB_WORKFLOW_REF": "fixture/repo/.github/workflows/smoke.yml@refs/heads/trusted",
                "GITHUB_WORKFLOW_SHA": "a" * 40, "GITHUB_JOB": "smoke", "GITHUB_RUN_ATTEMPT": "1",
            }
            plan["ciIdentity"] = {
                "repository": env["GITHUB_REPOSITORY"], "repositoryId": env["GITHUB_REPOSITORY_ID"],
                "workflowRef": env["GITHUB_WORKFLOW_REF"], "workflowSha": env["GITHUB_WORKFLOW_SHA"],
                "job": env["GITHUB_JOB"], "attempt": env["GITHUB_RUN_ATTEMPT"],
            }
        else:
            env = {
                "TF_BUILD": "True", "BUILD_BUILDID": "42", "BUILD_SOURCEVERSION": "a" * 40,
                "SYSTEM_COLLECTIONURI": "https://dev.azure.com/fixture/",
                "SYSTEM_COLLECTIONID": "collection", "SYSTEM_TEAMPROJECTID": "project",
                "SYSTEM_TEAMPROJECT": "fixture-project",
                "BUILD_REPOSITORY_ID": "repository", "BUILD_REPOSITORY_PROVIDER": "TfsGit",
                "SYSTEM_DEFINITIONID": "definition", "SYSTEM_JOBID": "job", "SYSTEM_JOBATTEMPT": "1",
            }
            plan["ciIdentity"] = {
                "collectionUri": env["SYSTEM_COLLECTIONURI"], "collectionId": env["SYSTEM_COLLECTIONID"],
                "projectId": env["SYSTEM_TEAMPROJECTID"], "repositoryId": env["BUILD_REPOSITORY_ID"],
                "repositoryProvider": env["BUILD_REPOSITORY_PROVIDER"], "definitionId": env["SYSTEM_DEFINITIONID"],
                "jobId": env["SYSTEM_JOBID"], "attempt": env["SYSTEM_JOBATTEMPT"],
            }
        env["AZD_SCENARIO_LIVE_APPROVAL_SHA256"] = "c" * 64
        return plan, env

    def test_example_keys_and_dataset_match_real_executor_for_both_providers(self):
        for provider in ("github", "azure-devops"):
            with self.subTest(provider=provider):
                plan, env = self.fixture(provider)
                result = self.service.validate_plan(plan, "c" * 64, env)
                self.assertEqual(result["provider"], provider)
                row = Path(plan["datasetFile"]).read_bytes()
                self.service.approved_row(row, plan["datasetSha256"], prompt=True)
                self.assertEqual(len(row.splitlines()), 1)

    def test_example_is_not_an_approved_or_runnable_plan(self):
        plan = sample.read_json(sample.HERE / "plan.example.json")
        self.assertFalse(plan["budgetControlExternallyVerified"])
        with self.assertRaises(self.service.Blocked):
            self.service.validate_plan(plan, "c" * 64, {})

    def test_missing_or_changed_dependency_blocks_without_starting_executor(self):
        for changed in (False, True):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as directory:
                root = Path(directory) / "dependency"
                if changed:
                    shutil.copytree(self.root, root)
                    (root / "eng" / "scripts" / "eval-scenario-ci" / "service.py").write_text("changed")
                output = Path(directory) / "evidence"
                with mock.patch.object(sample.subprocess, "run") as command, redirect_stderr(io.StringIO()):
                    self.assertEqual(sample.execute(root, Path("unused"), output), 3)
                command.assert_not_called()
                report = sample.read_json(output / "service-status.json")
                self.assertEqual((report["status"], report["execution"]), ("BLOCKED", "NOT RUN"))

    def test_existing_evidence_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            receipt = output / "service-status.json"
            receipt.write_text("existing evidence")
            with mock.patch.object(sample.subprocess, "run") as command, redirect_stderr(io.StringIO()):
                self.assertEqual(sample.execute(self.root, Path("unused"), output), 3)
            command.assert_not_called()
            self.assertEqual(receipt.read_text(), "existing evidence")

    def test_bad_plan_and_missing_protected_inputs_never_launch_or_disclose_content(self):
        secret_url = "https://user:private-password@example.invalid/?sig=private-token#private-fragment"
        plans = [secret_url, '{"mode":"other"}', '{"mode":"x","mode":"existing-agent-cli-evaluation"}',
                 json.dumps({"mode": sample.MODE})]
        for raw in plans:
            with self.subTest(raw=raw), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                path = root / "plan.json"
                path.write_text(raw)
                stderr = io.StringIO()
                with mock.patch.dict(os.environ, {}, clear=True), \
                     mock.patch.object(sample.subprocess, "run") as command, redirect_stderr(stderr):
                    self.assertEqual(sample.execute(self.root, path, root / "evidence"), 3)
                command.assert_not_called()
                public = stderr.getvalue() + (root / "evidence" / "service-status.json").read_text()
                for sensitive in ("private-password", "private-token", "private-fragment"):
                    self.assertNotIn(sensitive, public)

    def test_both_forms_share_adapter_and_publish_only_the_receipt(self):
        for name in ("github/action.yml", "azure-pipelines.steps.yml"):
            text = (sample.HERE / name).read_text()
            self.assertIn("run.py", text)
            self.assertIn("--executor-root", text)
            self.assertIn("--plan", text)
            self.assertIn("--output", text)
            self.assertIn("/service-status.json", text)
            self.assertNotIn("continue-on-error", text)
            self.assertNotIn("continueOnError", text)
            self.assertNotIn("auth login", text)

    def test_executor_exit_codes_and_arguments_are_preserved_without_a_shell(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            plan = root / "approved plan.json"
            plan.write_text(json.dumps({"mode": sample.MODE}))
            env = {"AZD_SCENARIO_LIVE_APPROVAL_SHA256": "c" * 64,
                   "AZD_SCENARIO_LIVE_AUTH_CONFIG": "caller-owned"}
            for exit_code in (0, 1, 3):
                with self.subTest(exit_code=exit_code), mock.patch.dict(os.environ, env, clear=True), \
                     mock.patch.object(sample.subprocess, "run",
                                       return_value=subprocess.CompletedProcess([], exit_code)) as command:
                    self.assertEqual(sample.execute(self.root, plan, root / "evidence"), exit_code)
                args = command.call_args.args[0]
                self.assertEqual(args[args.index("--plan") + 1], str(plan.resolve()))
                self.assertEqual(args[args.index("--output") + 1], str((root / "evidence").resolve()))
                self.assertNotIn("shell", command.call_args.kwargs)
                self.assertEqual(command.call_args.kwargs["stdin"], subprocess.DEVNULL)

    def test_real_process_imports_dependencies_and_blocks_before_cli_or_network(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            plan, _ = self.fixture()
            path = root / "plan.json"
            path.write_text(json.dumps(plan))
            env = {**os.environ, "AZD_SCENARIO_LIVE_APPROVAL_SHA256": "not-an-approval",
                   "AZD_SCENARIO_LIVE_AUTH_CONFIG": str(root / "absent-auth")}
            result = subprocess.run(
                [sys.executable, "-B", str(sample.HERE / "run.py"), "--executor-root", str(self.root),
                 "--plan", str(path), "--output", str(root / "evidence")],
                env=env, stdin=subprocess.DEVNULL, capture_output=True, timeout=15, check=False,
            )
            self.assertEqual(result.returncode, 3, result.stderr.decode())
            receipt = sample.read_json(root / "evidence" / "service-status.json")
            self.assertEqual((receipt["status"], receipt["execution"]), ("BLOCKED", "NOT RUN"))
            self.assertNotIn("commands", receipt)


if __name__ == "__main__":
    unittest.main()
