# Fixture provenance

These fixtures are **schema-derived, unverified against a live cluster**.

They were written by hand from the `api/v0045` generated types in
`github.com/SlinkyProject/slurm-client` v1.2.2 (which are generated from
the slurmrestd OpenAPI schema for data_parser v0.0.45). No containerized
Slurm 26.05 image with slurmrestd was available at authoring time
(`giovtorres/slurm-docker-cluster` tops out at Slurm 25.11, i.e.
v0.0.44), so no live responses could be recorded. When a Slurm 26.05
environment is available, replace these with recorded responses (scrub
hostnames) and update this file.
