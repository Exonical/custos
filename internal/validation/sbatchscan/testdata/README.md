# sbatchscan test fixtures

`sbatch-26.05-options.txt` — the full long-option list for sbatch,
extracted from the SchedMD `sbatch` man page for **Slurm 26.05**
(https://slurm.schedmd.com/sbatch.html, "Version 26.05" navigation).
Recorded 2026-09; derived from published documentation, not captured
from a live cluster. `TestOptionTableCoversFixture` asserts every option
in the fixture is in the table and vice versa.

`bypass/*.sh` — the directive-bypass corpus (docs/script-validation.md,
Testing item 1). Each file carries a header comment:

```text
# expect: field=<canonical-field|none> code=<CUSTOSxxx|CUSTOS011>
```

`field`/`code` describe the expected dominant finding; heredoc/string
cases expect `code=CUSTOS011` with no `SECURITY_VIOLATION`.
