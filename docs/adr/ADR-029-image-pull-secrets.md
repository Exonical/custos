# ADR-029: Image pull secrets for container tasks

Status: Draft  
Date: 2026-10-04

## Context

Container images may live in private OCI registries. Giving a task's image
credentials to the user process, recording them in a workflow version, or
placing them in a generated shell command would expose registry credentials
past the pull operation. Apptainer and Pyxis also use different pull
mechanisms, so credentials must be available before the container starts.

## Decision

- A task may declare `image.pullSecret` with either a literal `username` or a
  `usernameSecret` handle, and a required `passwordSecret` handle. Both handles
  must be declared in `spec.secrets` with `use: image_pull`; the referenced
  SecretReferences must be generic and allow `image_pull`. Image-pull handles
  are rejected if unused, cannot specify `envName`, and cannot be referenced
  from task environment templates. Literal usernames are limited to 256
  characters and reject whitespace and control characters.
- `pullSecret` is supported for `docker://` images on Apptainer or Pyxis, and
  for `oras://` images on Apptainer. Absolute paths and Pyxis `oras://` pulls
  are rejected. Apptainer uses `APPTAINER_DOCKER_USERNAME` and
  `APPTAINER_DOCKER_PASSWORD` for both supported URI schemes.
- A pull-secret task must be single-node. Static validation rejects a
  `multinode` block or resolved `resources.nodes > 1`; admission repeats the
  check on resolved resources. Arrays remain supported.
- Admission freezes the literal username, runtime-derived registry host, and
  pull mode, plus only the secret handle/reference UUIDs in the existing
  submit-time secret references. Secret values are resolved immediately before
  Slurm submission into reserved `CUSTOS_IMAGE_PULL_*` variables. They are
  never serialized into `ExecutionSpec` or included in the wrapper script,
  command arguments, logs, task environment, or Pyxis `--container-env`.
- For Apptainer, the wrapper performs `apptainer pull --disable-cache` to
  `$CUSTOS_JOB_DIR/image.sif`, scopes registry credentials to that command,
  unsets the internal pull variables, and runs the local SIF with the existing
  `--no-eval` and job-directory bind.
- For Pyxis, the wrapper creates a host-only temporary Enroot configuration
  directory with mode `0700` and a `.credentials` file with mode `0600`. It
  writes netrc entries using `printf '%s'` from the internal environment
  variables; Docker Hub receives entries for both `auth.docker.io` and
  `registry-1.docker.io`. It runs `enroot import` to
  `$CUSTOS_JOB_DIR/image.sqsh`, removes the temporary directory, unsets the pull
  variables, then invokes srun with the local squashfs image. The EXIT trap
  also removes the temporary directory if import fails. Pyxis clusters using
  pull secrets must have the `enroot` CLI available on compute nodes.
- Image-pull deliveries emit the existing value-free `secret.accessed` audit
  event with purpose `image_pull`.

## Alternatives considered

- **Pass credentials through the image's environment:** rejected because the
  user process would inherit registry credentials.
- **Put Pyxis credentials in the mounted job directory:** rejected because the
  job directory is mounted into the container and is readable by the job's
  Unix user.
- **Support multi-node pull-secret tasks:** deferred. The wrapper does not
  distribute the private image across nodes, and pull credentials must be
  removed before user code starts.

## Consequences

- Pull-secret values exist in the Slurm submission environment until the
  wrapper starts and removes them. Slurm administrators and authorized job
  owners may be able to inspect that environment while a job is pending or
  starting. Sites must account for this residual exposure.
- Pyxis imports a local `.sqsh` before launching the container, so `enroot`
  must be installed on the compute node. Apptainer uses a local SIF for the
  task launch; its pull operation requires Apptainer 1.1 or later for the
  existing `--no-eval` execution path.
- Pull-secret configuration and secret reference metadata are frozen with the
  admitted execution, but secret values are not persisted by Custos.
