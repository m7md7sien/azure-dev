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

    def test_older_python_blocks_before_dependency_or_plan_validation(self):
        for version in ((3, 8), (3, 10), (3, 11)):
            with self.subTest(version=version), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / "evidence"
                with mock.patch.object(sample.sys, "version_info", version), \
                     mock.patch.object(sample, "verified_entrypoint") as verify, \
                     mock.patch.object(sample.subprocess, "run") as command, redirect_stderr(io.StringIO()):
                    self.assertEqual(sample.execute(Path("unused"), Path("unused"), output), 3)
                verify.assert_not_called()
                command.assert_not_called()
                report = sample.read_json(output / "service-status.json")
                self.assertEqual((report["status"], report["execution"]), ("BLOCKED", "NOT RUN"))
                self.assertIn("Python 3.12", report["error"]["message"])

    def test_dependency_complete_profile_is_currently_blocked_before_driver(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            plan, env = self.fixture()
            core = root / "azd.exe"
            core.write_bytes(b"not an executable")
            plan["azdExecutable"] = str(core)
            plan["binarySha256"]["azd"] = hashlib.sha256(core.read_bytes()).hexdigest()
            installed = {}
            for extension, command in self.service.required_extensions(plan).items():
                binary = root / (command + ".exe")
                binary.write_bytes(b"not an extension")
                plan["binarySha256"][extension] = hashlib.sha256(binary.read_bytes()).hexdigest()
                installed[extension] = {
                    "id": extension, "namespace": "ai." + command,
                    "version": plan["versions"][extension], "path": binary.name,
                }
            config = root / "config.json"
            config.write_text(json.dumps({"extension": {"installed": installed}}))
            self.service.verify_install(plan, root)
            for command in ("inspector", "projects", "connections", "toolboxes"):
                extension = "azure.ai." + command
                installed[extension] = {
                    "id": extension, "namespace": "ai." + command,
                    "version": "mock-only", "path": command + ".exe",
                }
                (root / (command + ".exe")).write_bytes(b"not a dependency executable")
            config.write_text(json.dumps({"extension": {"installed": installed}}))
            path = root / "plan.json"
            raw = json.dumps(plan).encode()
            path.write_bytes(raw)
            env.update(AZD_SCENARIO_LIVE_APPROVAL_SHA256=hashlib.sha256(raw).hexdigest(),
                       AZD_SCENARIO_LIVE_AUTH_CONFIG=str(root))
            with mock.patch.object(self.service, "Driver") as driver:
                with self.assertRaisesRegex(self.service.Blocked, "exactly the mode-specific"):
                    self.service.execute(path, root / "evidence", env)
            driver.assert_not_called()
            report = sample.read_json(root / "evidence" / "service-status.json")
            self.assertEqual((report["status"], report["execution"]), ("BLOCKED", "NOT RUN"))

    def copy_minimal_dependency(self, root):
        for relative in sample.read_json(sample.HERE / "dependency.json")["files"]:
            target = root / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(self.root / relative, target)

    def test_each_missing_or_changed_module_blocks_without_starting_executor(self):
        files = sample.read_json(sample.HERE / "dependency.json")["files"]
        for relative in files:
            for changed in (False, True):
                with self.subTest(module=relative, changed=changed), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory) / "dependency"
                    self.copy_minimal_dependency(root)
                    if changed:
                        (root / relative).write_text("changed")
                    else:
                        (root / relative).unlink()
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
        github = (sample.HERE / "github" / "action.yml").read_text()
        ado = (sample.HERE / "azure-pipelines.steps.yml").read_text()
        self.assertIn("if [ -e \"$SAMPLE_OUTPUT\" ]", github)
        self.assertIn("steps.lifecycle.outputs.fresh_output == 'true'", github)
        self.assertIn("Test-Path -LiteralPath $env:SAMPLE_OUTPUT", ado)
        self.assertIn("and(always(), eq(variables['SampleFreshOutput'], 'true'))", ado)
        self.assertIn("exit 3", ado)

    def readme_powershell(self, heading):
        text = (sample.HERE / "README.md").read_text()
        section = text.split(heading, 1)[1]
        return section.split("```powershell\n", 1)[1].split("```", 1)[0]

    def powershell(self):
        executable = shutil.which("pwsh")
        self.assertIsNotNone(executable, "PowerShell 7 is required for the documented recipe tests")
        return executable

    def test_install_recipe_checks_both_hashes_before_first_azd_command(self):
        script = self.readme_powershell("### Non-interactive installation contract")
        for invalid in ("core", "registry", "digest-shape", None):
            with self.subTest(invalid=invalid), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                marker = root / "azd-called"
                core = root / "azd.ps1"
                core.write_text('Set-Content -LiteralPath $env:SAMPLE_TEST_MARKER -Value invoked\nexit 0\n')
                registry = root / "registry.json"
                registry.write_text('{"extensions":[]}')
                env = {**os.environ, "APPROVED_AZD": str(core),
                       "APPROVED_AZD_SHA256": hashlib.sha256(core.read_bytes()).hexdigest(),
                       "APPROVED_REGISTRY": str(registry),
                       "APPROVED_REGISTRY_SHA256": hashlib.sha256(registry.read_bytes()).hexdigest(),
                       "APPROVED_AGENTS_VERSION": "mock-only", "APPROVED_EVALUATIONS_VERSION": "mock-only",
                       "APPROVED_DATASET_VERSION": "mock-only", "AZD_CONFIG_DIR": str(root / "profile"),
                       "SAMPLE_TEST_MARKER": str(marker)}
                if invalid == "core":
                    env["APPROVED_AZD_SHA256"] = "0" * 64
                elif invalid == "registry":
                    env["APPROVED_REGISTRY_SHA256"] = "0" * 64
                elif invalid == "digest-shape":
                    env["APPROVED_AZD_SHA256"] = "not-a-digest"
                result = subprocess.run(
                    [self.powershell(), "-NoProfile", "-NonInteractive", "-Command", script],
                    env=env, stdin=subprocess.DEVNULL, capture_output=True, timeout=30, check=False,
                )
                if invalid is None:
                    self.assertEqual(result.returncode, 0, result.stderr.decode())
                    self.assertTrue(marker.exists())
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(marker.exists(), "azd must not execute before both approvals match")

    def test_ado_stale_output_exits_three_without_launch_or_upload_permission(self):
        text = (sample.HERE / "azure-pipelines.steps.yml").read_text()
        body = text.split("  - pwsh: |\n", 1)[1].split("    displayName:", 1)[0]
        script = "\n".join(line[6:] for line in body.splitlines())
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "evidence"
            output.mkdir()
            receipt = output / "service-status.json"
            receipt.write_text("prior evidence")
            env = {**os.environ, "SAMPLE_OUTPUT": str(output), "SAMPLE_DIRECTORY": "must-not-run"}
            result = subprocess.run(
                [self.powershell(), "-NoProfile", "-NonInteractive", "-Command", script],
                env=env, stdin=subprocess.DEVNULL, capture_output=True, timeout=15, check=False,
            )
            self.assertEqual(result.returncode, 3, result.stderr.decode())
            self.assertIn(b"SampleFreshOutput]false", result.stdout)
            self.assertNotIn(b"SampleFreshOutput]true", result.stdout)
            self.assertEqual(receipt.read_text(), "prior evidence")

    def test_readme_export_selects_only_manifest_files(self):
        script = self.readme_powershell("## Files and dependency assembly")
        self.assertIn("$paths = @($dependency.files.PSObject.Properties.Name)", script)
        self.assertIn('$commit @paths', script)
        self.assertNotIn("eng/scripts/eval-scenario-ci eng/scripts/eval-candidate-proof", script)
        readme = (sample.HERE / "README.md").read_text()
        self.assertIn("- template: /cli/azd/", readme)
        self.assertIn("override `sampleDirectory`", readme)

    def test_process_start_failure_records_blocked_not_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "plan.json"
            path.write_text(json.dumps({"mode": sample.MODE}))
            env = {"AZD_SCENARIO_LIVE_APPROVAL_SHA256": "c" * 64,
                   "AZD_SCENARIO_LIVE_AUTH_CONFIG": "caller-owned"}
            with mock.patch.dict(os.environ, env, clear=True), \
                 mock.patch.object(sample.subprocess, "run", side_effect=OSError("private path")), \
                 redirect_stderr(io.StringIO()) as stderr:
                self.assertEqual(sample.execute(self.root, path, root / "evidence"), 3)
            receipt = sample.read_json(root / "evidence" / "service-status.json")
            self.assertEqual((receipt["status"], receipt["execution"]), ("BLOCKED", "NOT RUN"))
            self.assertNotIn("private path", stderr.getvalue() + json.dumps(receipt))

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
            dependency = root / "minimal-dependency"
            self.copy_minimal_dependency(dependency)
            plan, _ = self.fixture()
            path = root / "plan.json"
            path.write_text(json.dumps(plan))
            env = {**os.environ, "AZD_SCENARIO_LIVE_APPROVAL_SHA256": "not-an-approval",
                   "AZD_SCENARIO_LIVE_AUTH_CONFIG": str(root / "absent-auth")}
            result = subprocess.run(
                [sys.executable, "-B", str(sample.HERE / "run.py"), "--executor-root", str(dependency),
                 "--plan", str(path), "--output", str(root / "evidence")],
                env=env, stdin=subprocess.DEVNULL, capture_output=True, timeout=15, check=False,
            )
            self.assertEqual(result.returncode, 3, result.stderr.decode())
            receipt = sample.read_json(root / "evidence" / "service-status.json")
            self.assertEqual((receipt["status"], receipt["execution"]), ("BLOCKED", "NOT RUN"))
            self.assertNotIn("commands", receipt)


if __name__ == "__main__":
    unittest.main()
