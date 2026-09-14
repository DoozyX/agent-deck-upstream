## Deployed-system verification

A verification entrance establishes whether a deployed system satisfies a
defined contract, with **no assumed edit**: do not enter implementation,
pull-request, CI, or deployment stages unless the outcome and authorized scope
explicitly permit delivery. Run these four phases before the per-task pipeline:

1. **Recon.** Record the deployed version/revision/digest; target
   environment/licensing state; authorized scope; stable arm IDs/questions
   (the question each arm answers); exact artifact paths and schemas; and a
   freshness cutoff. Define what evidence distinguishes product behavior from
   harness, environment, and license failures. Recon writes the arm schema to
   one file — `$RUN_DIR/arms/schema.md` unless you declare otherwise — in
   whatever format the arms will actually produce. Pass that path as
   `ARM_SCHEMA_PATH` to every arm and to the report child. One declared
   schema file, named the same way for everyone, is what stops a downstream
   child from validating against a shape it imagined.
2. **Independent measurement arms.** Launch the arms in parallel so they do
   not share conclusions or contaminate one another's evidence. Each producer
   writes its machine-readable artifact, finishes, and reports the artifact
   path; a path reported before producer completion is not ready for use.
3. **Conductor validation and adjudication.** Before reading even deciding
   fields, validate each artifact against the declared schema file, plus its
   provenance, producer completion, and freshness against recon. Read only
   the deciding fields where possible. Adjudicate contradictions rather than selecting the convenient
   result. For a flaky external measurement, preserve and diagnose the first
   failure evidence, then permit at most **one clean rerun** by default. A
   second failure is a product `defect` when it demonstrates product behavior,
   or `inconclusive` when the harness, environment, or license prevents a
   trustworthy decision.
4. **Consolidated report.** Record deployed identity, environment, authorized
   scope, arm questions and evidence, artifact-validation results,
   contradictions and their adjudication, and exactly one outcome: `pass`,
   `defect`, or `inconclusive`. A `pass` is terminal with no edits, pull
   request, CI run, or deployment. A `defect` enters the delivery pipeline only
   when the defect is within the authorized scope. An `inconclusive` result
   terminates honestly with what blocked a trustworthy decision; do not claim
   success or retry indefinitely. `inconclusive` is for evidence that is
   missing, stale, unattributable or contradictory — **never for packaging**.
   A report child that rules against a run because the arms are Markdown and
   it expected JSON, without challenging a single measurement, has answered
   the wrong question; the declared schema file wins, and a shape mismatch
   comes back to you as a question.

The existing child and conductor rotation/handoff rules apply throughout this
flow. All downstream PR and CI language applies only when an in-scope `defect`
enters delivery; verification-only `pass` and `inconclusive` outcomes stop
before those stages.
