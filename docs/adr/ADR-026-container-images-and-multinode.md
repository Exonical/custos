# ADR-026: Container images, inline scripts, and multinode tasks

Status: Draft  
Date: 2026-09-28

## Context

HPC workflows often run an existing application inside an Apptainer or Pyxis
image and use Slurm's `srun` to coordinate MPI ranks. Generic distributed
programs need a host list and a remote-launch hook, while shell scripts are
most useful inline beside the task that executes them. The existing
`custos.io/v1alpha1` task model has neither container/runtime metadata nor a
resource model that distinguishes per-node CPU slots from per-rank CPUs.

## Decision

- Images are optional per task. Without `image`, tasks retain the existing
  host execution path. A task image URI is a validated `docker://`, `oras://`,
  or absolute path reference. The target cluster selects `apptainer` or
  `pyxis` and may enforce URI prefixes and sha256 digest pinning; images remain
  user-selected code that runs as the Slurm job user.
- Cluster `container_runtime` is nullable. The platform API stores its type,
  Apptainer binary, image-prefix policy, digest requirement, `slurm_in_container`
  capability, and MPI plugin. The tenant-visible cluster summary exposes only
  the runtime type. There are no site bind-path settings: both runtimes mount
  only `$CUSTOS_JOB_DIR`.
- Apptainer launches with `exec --no-eval` and therefore requires Apptainer
  1.1 or later; Pyxis receives its container flags on `srun`. Pyxis rejects
  `oras://`, passes absolute paths unchanged, strips `docker://`, and
  translates registry-qualified `host/repository` references to
  `host#repository`. The wrapper quotes image and environment values.
- An image task's user environment is not sent in `JobSubmission.Environment`
  or exported on the host. Both runtimes receive user variables through
  `/usr/bin/env KEY=VALUE` inside the image, so the image must include
  `/usr/bin/env`. Pyxis `--container-env` is reserved for runtime, secret, and
  `MULTINODE_*` names whose host-provided values must override image defaults.
  `PATH` and `LD_LIBRARY_PATH` are user-configurable only for image tasks.
  Custos, Slurm, container-runtime, and generated names remain controlled.
  Controlled, runtime, and secret environment values stay host-side.
- Scripts may be content-addressed references or inline text in the immutable
  workflow spec. Inline source is not rewritten into a reference: create/update
  idempotently stores the bytes by digest, `TaskSnapshot` validates the inline
  bytes directly, and admission puts and verifies the digest again before
  freezing the payload reference. Language is explicit when supplied and is
  otherwise inferred from the inline shebang without mutating the spec.
- `env` accepts the existing map and a `NAME=VALUE` list shorthand. The list
  splits on the first equals, rejects invalid/duplicate names, and canonical
  storage uses the map form.
- `resources.cpuAffinity` accepts `none`, `core`, `socket`, or `numa`, mapped
  to `--cpu-bind=cores|sockets|ldoms` for srun. When a batch wrapper has no
  final srun, it exports controlled `SLURM_CPU_BIND` so nested srun steps
  inherit the affinity.
- A `multinode` block carries `nodes`, `implementation`, and optional
  `procsPerNode`. For OpenMPI/MPICH, `resources.cpu` is CPUs per node (default
  one), `procsPerNode` defaults to that CPU count, and ranks are
  `nodes × procsPerNode`; CPUs per rank are `cpu / procsPerNode`, which must
  divide evenly. `resources.memory` remains per node. The wrapper uses
  `srun --mpi=<mpi_plugin>` for these ranks. Multinode conflicts with arrays
  and explicit `resources.nodes`, `tasks`, or `tasksPerNode`; an explicit
  launch must agree with the implementation.
- Generic multinode runs the command once with one Slurm task per node and
  slot count `procsPerNode` (default `resources.cpu`). Custos exports the
  Fuzzball-compatible `MULTINODE_*` variables and generates an `srun
  --overlap` rsh wrapper; it never starts SSH. A generic image requires
  `slurm_in_container` and a compatible Slurm client in the image because
  the remote launcher executes from inside the container.
- Srun-launched payloads are written and verified as `payload.in`, then
  distributed to every allocated node with `sbcast --force --preserve` into
  each node's `$CUSTOS_JOB_DIR/payload`. Batch-only launches move the verified
  payload locally. The wrapper does not `exec` its final command, so the EXIT
  trap removes the job directory.
- Sbatch export emits inline script bodies, but returns
  `EXPORT_UNSUPPORTED` for image and multinode tasks: runtime selection and
  launcher policy are cluster-specific and cannot be represented portably.

## Alternatives considered

- **Require a container on every task:** rejected; non-image tasks must behave
  exactly as they do today.
- **Store inline source only in the script table:** rejected; the immutable
  workflow must retain the authored source. The content-addressed copy exists
  so admission and job submission continue to use the payload digest path.
- **Launch generic remote work over SSH:** rejected; remote steps use Slurm
  `srun --overlap` and the generated rsh wrapper, without user-controlled
  shell interpolation.
- **Configure site bind paths in each workflow:** rejected; bindings are
  cluster-owned policy, and this phase intentionally exposes no site bind
  configuration. Only the per-job directory is mounted.
- **Export image/multinode jobs as standalone sbatch scripts:** rejected;
  export cannot carry the target cluster's runtime or multinode launcher
  guarantees.

## Consequences

- Existing non-image task specs keep their canonical hashes and host
  execution behavior. New fields are optional and omitted from canonical
  output when unset.
- Image provenance remains a site/user responsibility; administrators should
  configure an image-prefix allow-list and require digest pinning where
  reproducibility or supply-chain control matters.
- Generic multinode payloads depend on the cluster's Slurm client and runtime
  configuration. Cluster runtime metadata and the frozen `ExecutionSpec`
  make those choices auditable at admission and job submission.
- Fuzzfile import is not included in this decision or implementation phase.
