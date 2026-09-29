# Evaluate an existing agent in CI

<!-- cspell:ignore COLLECTIONURI COLLECTIONID DEFINITIONID JOBID JOBATTEMPT SOURCEVERSION -->

**Local integration sample, not a published turnkey workflow.** This sample
invokes one approved, existing hosted agent through `azd ai agent invoke`, then
evaluates its actual response. It does not create, deploy, update or delete the
agent. Full agent deployment CI/CD still needs a separate validated
project/provider/deployment/teardown contract.

The shared executor is a dependency of [production CI #10178](https://github.com/Azure/azure-dev/pull/10178),
not duplicated here. The corrected local dependency is
`eb9ce7c3e8e114b62304fb8fcf794ee3a82672f2`.
It is **not published** by this sample and is not assumed present on `main`.
No downloadable URL, approved release selection, cloud execution or new PR is
claimed. Do not enable this sample until an authorized owner publishes and
reviews both the executor and a compatible package tuple.

## Files and dependency assembly

| File | Purpose |
| --- | --- |
| [dependency.json](dependency.json) | Exact local source commit and SHA256 of every production Python module needed by this path |
| [run.py](run.py) | Verify dependency bytes and forward the plan unchanged to the single shared executor |
| [github/action.yml](github/action.yml) | GitHub composite action for an already protected, prepared job |
| [azure-pipelines.steps.yml](azure-pipelines.steps.yml) | ADO steps template for the same prepared-job contract |
| [plan.example.json](plan.example.json) | Deliberately incomplete, unapproved plan shape; not credentials or a runnable default |
| [datasets/support.jsonl](datasets/support.jsonl) | One manually curated support question and expected behavior |
| [test_sample.py](test_sample.py) | Offline adapter, real plan-validator and refusal checks |

The minimal dependency map is the four files in `dependency.json`: `service.py`,
`scenario.py`, `http_transport.py`, and `eval-candidate-proof/verify.py`, preserving
their `eng/scripts/` layout. No production workflow, installer fixture, release
pin or shared file is edited or copied into this sample. An authorized integrator
can merge the production work, or stage a separate, fresh dependency checkout at
the recorded commit. Pass its root as `--executor-root`.
Use a fresh LF-byte export, not a working directory containing unrelated Python
modules or bytecode caches. Git's automatic CRLF conversion changes the pinned
bytes and is deliberately rejected. The recorded hash map is not itself an approval authority:
review and protect this sample and its caller as well as the executor.

For local development, when the recorded commit is already in your own Git
object database, export it without changing branches:

```powershell
$commit = "eb9ce7c3e8e114b62304fb8fcf794ee3a82672f2"
$stage = Join-Path ([IO.Path]::GetTempPath()) ("agent-eval-" + [guid]::NewGuid())
git -c core.autocrlf=false archive --format=zip --output="$stage.zip" $commit `
  eng/scripts/eval-scenario-ci eng/scripts/eval-candidate-proof
if ($LASTEXITCODE -ne 0) { throw "The exact local dependency is unavailable" }
Expand-Archive -LiteralPath "$stage.zip" -DestinationPath $stage
$env:SAMPLE_EXECUTOR_ROOT = $stage
python -B -m unittest discover `
  -s cli/azd/extensions/azure.ai.evaluations/samples/agent-evaluation-ci -p "test_*.py" -v
```

This export is local validation, not publication. Tests call the real plan
validator and a real subprocess refusal path, but mock successful execution.
They do not log in, install binaries or contact Azure.

## Prepare the protected job

Both provider forms are **steps**, not standalone pipelines. They intentionally
have no triggers, login, infrastructure provisioning or placeholder setup steps.
Use a trusted reviewed workflow revision; never run modified PR scripts with
service credentials. A PR may request evaluation, but approval, executor and
setup code must come from the trusted side of that boundary.

Before including either form, the existing authorized job must supply:

1. Python 3.12 or later and the exact executor export described above.
2. An exclusive, isolated `AZD_CONFIG_DIR` with an existing approved CI service
   identity and exactly the approved `azure.ai.agents`, `azure.ai.evaluations`
   and `azure.ai.dataset` installations. The `extensions.ai-agents` namespace
   must initially be absent. Never copy a developer's credential cache.
3. A compatible, explicitly approved core/agents/evaluations/dataset artifact
   tuple: immutable source, registry/archive digests, installed executable hashes
   and exact versions. The referenced agents implementation requires core
   `>=1.34.2`; the historical offline core `1.33.0` tuple is incompatible.
   This sample selects no replacement version and changes no official pins.
4. An existing project endpoint, service-principal client and access tenant,
   hosted agent name/version, supported built-in evaluator and existing judge
   deployment. Confirm authority for each exact operation listed in the plan,
   including cleanup. The plan requires an approved positive budget and a
   reference to an externally verified spend control. Counts/timeouts are not
   monetary enforcement.
5. An externally approved per-run plan, staged as a private file, and its exact
   digest from protected configuration. Approval covers the native provider,
   repository/workflow/job/attempt/run/revision, expiry and every resource/input
   field. Do not compute the digest in an untrusted job and treat it as approval.

Use existing federated identity/service-connection setup. The GitHub parent job
must bind an existing, independently confirmed reviewer-protected environment;
the ADO parent must use already approved environment/service-connection checks.
Creating an environment with a convenient name is not an approval gate. This
sample creates no identities, IAM grants, environments or runners.

### Non-interactive installation contract

The authorized setup stage can install from an independently reviewed immutable
registry using the supported commands below. All values must already have been
selected, verified and staged by its owner. This is an installation recipe, not
an implemented auth/bootstrap stage in these templates.

```powershell
$required = @("APPROVED_AZD", "APPROVED_REGISTRY", "APPROVED_AGENTS_VERSION",
              "APPROVED_EVALUATIONS_VERSION", "APPROVED_DATASET_VERSION", "AZD_CONFIG_DIR")
foreach ($name in $required) {
  if (-not [Environment]::GetEnvironmentVariable($name)) { throw "Missing protected input: $name" }
}
& $env:APPROVED_AZD extension source add --name sample-approved --type file `
  --location $env:APPROVED_REGISTRY --no-prompt
if ($LASTEXITCODE -ne 0) { throw "Could not register the approved source" }
& $env:APPROVED_AZD extension install azure.ai.agents `
  --source sample-approved --version $env:APPROVED_AGENTS_VERSION --no-prompt
if ($LASTEXITCODE -ne 0) { throw "Agents installation failed" }
& $env:APPROVED_AZD extension install azure.ai.evaluations `
  --source sample-approved --version $env:APPROVED_EVALUATIONS_VERSION --no-prompt
if ($LASTEXITCODE -ne 0) { throw "Evaluations installation failed" }
& $env:APPROVED_AZD extension install azure.ai.dataset `
  --source sample-approved --version $env:APPROVED_DATASET_VERSION --no-prompt
if ($LASTEXITCODE -ne 0) { throw "Dataset installation failed" }
```

Verify the core and registry/archive bytes **before execution**, then approve the
installed paths, hashes and versions in the plan. The executor rechecks installed
bytes/routing before making service calls. Do not use `latest`, infer approval
from publisher hashes, disable dependencies to conceal incompatibility, or
silently upgrade. If installation introduces other extension routes, the
exclusive-profile contract is not met: resolve that package compatibility with
the executor owner instead of deleting dependencies.

## Author the plan and small test case

Start from `plan.example.json` outside the repository. Empty strings and the
false budget-control flag intentionally make it fail closed. Fill every field
through the protected approval process; never commit the completed plan.

This test case expects an existing support agent that accepts a top-level
`query` string and returns a top-level `answer` string as synchronous HTTP200
JSON. The agent should advise checking the delivery location and contacting
support, without promising refunds or requesting payment details. Change the
approved field names if its contract differs. Streaming, HTTP202 and nested
response fields are not supported by this executor. There is no sample server
or deployment manifest because deploying a new hosted agent is not implemented.

Set `datasetFile` to the absolute staged path of `datasets/support.jsonl` and
`datasetSha256` to its exact raw SHA256, including the final newline. It must
contain exactly one UTF-8 object with nonempty `query` and `ground_truth`.
The executor creates the actual static evaluation configuration using the
approved evaluator/judge and its own registered dataset identity; this sample
does not carry a second evaluation engine or predict an agent response.

Choose a built-in evaluator supported by the **approved installed version** and
appropriate for this row. Neither `builtin.*` syntax nor local schema validation
proves service availability. No evaluator or model is silently selected.

The GitHub identity keys are shown in the plan. For ADO, set `provider` to
`azure-devops` and replace `ciIdentity` completely with these keys, bound to the
native variables by the shared validator:

| ADO plan key | Native variable |
| --- | --- |
| `collectionUri` | `SYSTEM_COLLECTIONURI` |
| `collectionId` | `SYSTEM_COLLECTIONID` |
| `projectId` | `SYSTEM_TEAMPROJECTID` |
| `repositoryId` | `BUILD_REPOSITORY_ID` |
| `repositoryProvider` | `BUILD_REPOSITORY_PROVIDER` |
| `definitionId` | `SYSTEM_DEFINITIONID` |
| `jobId` | `SYSTEM_JOBID` |
| `attempt` | `SYSTEM_JOBATTEMPT` |

`runId` is `GITHUB_RUN_ID` or `BUILD_BUILDID`; `workflowCommit` is `GITHUB_SHA` or
`BUILD_SOURCEVERSION`. Expiry must leave the configured observation and cleanup
windows plus setup overhead, while remaining within 24 hours. Each attempt needs
its own plan; do not reuse a plan from a previous run.

## Include one provider form

These snippets belong **after protected staging in the same job**. Paths below
are staging conventions, not existing resources. The parent job needs at least
30 minutes for the executor's bounded observation, cleanup and setup. Do not
cancel an active service job just because a newer PR commit arrives.

GitHub: declare `permissions: {contents: read}` on the parent workflow. Grant
`id-token: write` only to the trusted bootstrap job if its existing federated
login needs it; the composite action does not request a token. Pass approval
through an environment secret and auth-profile location through protected
configuration, not public workflow inputs.

```yaml
- uses: ./cli/azd/extensions/azure.ai.evaluations/samples/agent-evaluation-ci/github
  with:
    executor-root: ${{ runner.temp }}/approved-executor
    plan: ${{ runner.temp }}/approved-plan.json
    output-directory: ${{ runner.temp }}/agent-evaluation-${{ github.run_attempt }}
  env:
    AZD_SCENARIO_LIVE_APPROVAL_SHA256: ${{ secrets.AZD_SCENARIO_LIVE_APPROVAL_SHA256 }}
    AZD_SCENARIO_LIVE_AUTH_CONFIG: ${{ vars.AZD_SCENARIO_LIVE_AUTH_CONFIG }}
```

ADO: configure `ScenarioLiveApprovalSha256` as a protected secret and
`ScenarioLiveAuthConfig` as the isolated staged profile path. Do not expose
these as queue-time overrides. The template maps them to the same executor
variables. No organization, pipeline ID, connection or pool is supplied here.

```yaml
- template: cli/azd/extensions/azure.ai.evaluations/samples/agent-evaluation-ci/azure-pipelines.steps.yml
  parameters:
    executorRoot: $(Agent.TempDirectory)/approved-executor
    plan: $(Agent.TempDirectory)/approved-plan.json
    outputDirectory: $(Agent.TempDirectory)/agent-evaluation-$(System.JobAttempt)
```

Both forms preserve nonzero exit status and publish **only**
`service-status.json`, including failures. They reject an existing output
directory before launch and suppress its upload, so an old PASS receipt cannot
be published as this attempt's evidence. The parent must never use
`continue-on-error` or `continueOnError` on this gate. Restrict artifact access:
receipts contain owned resource IDs, although not credential caches, prompts,
raw responses or the approved plan. GitHub retention is seven days; configure
ADO retention through the existing pipeline policy.

## Results, quality and cleanup

| Result | Meaning |
| --- | --- |
| Exit 0, `PASS` / `COMPLETED` | Invocation, evaluation assertions and every owned cleanup succeeded |
| Exit 3, `BLOCKED` / `NOT RUN` | Missing or invalid dependency/plan/identity/artifact prerequisites; no successful evaluation inferred |
| Exit 1, `FAIL` | Started service execution or cleanup failed; inspect the receipt, not only the exit code |
| Missing receipt or process/job cancellation | Infrastructure interruption or unknown outcome; never a quality pass |

The gate requires one completed run bound to the actual invocation response,
with integer counts `total=1`, `passed=1`, and zero failed, errored and skipped
rows. A completed run alone or a high scored-row pass rate is insufficient.
`quality: PASS` is recorded only after those assertions; the overall status
becomes PASS only after cleanup. A missing quality result can mean a quality
failure **or** an earlier execution failure; this version has no stable separate
quality-regression exit code. Do not invent one or parse error prose as an API.

The receipt includes `planSha256`, native execution identity, commands/timings,
`agentCliInvocation`, owned session/dataset/evaluation/run IDs, and separate
`remoteCleanup`, `datasetCleanup`, `sessionCleanup`, `agentStateCleanup` and local
`cleanup` outcomes. Primary and cleanup failures are retained separately.

Cleanup attempts only the returned owned session ID, evaluation ID and dataset
name/version, with one shared deadline, and removes only the initially absent
local agents namespace and verifies its removal. It never deletes the existing agent,
project, model or caller's auth profile. The parent owns profile disposal.
An ambiguous create is not retried or cleaned by guessed identity: follow the
receipt's manual-reconciliation requirement.

Logical deletion does not prove physical blob removal, remote cancellation or
stopped billing. One invocation, one row and one run still incur inference,
evaluation and possible storage/hosting charges. No live cost measurement or
service run is claimed. GitHub/ADO activation, compatible published artifacts,
existing identity/resource/spend approval and full deployment remain external
dependencies.
