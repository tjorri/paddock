# Internal

Execution artifacts and internal-audience documentation. Content here is
not part of the public user-facing docs. Some of it may migrate back out
to the external sections (e.g., `operations/`, `security/`) as those
sections fill in.

Subdirectories:

- [`specs/`](specs/) — version-tagged implementation specs (scope,
  acceptance criteria, file-change manifests). One per major feature.
- [`migrations/`](migrations/) — historical upgrade guides between
  Paddock versions. Future upgrade guidance lives in
  [`../operations/`](../operations/) once written.
- [`observability/`](observability/) — internal observability notes.
  Source material for the future external `operations/audit.md` and
  `operations/monitoring.md`.
- [`security-audits/`](security-audits/) — security audit reports and
  tool output.
