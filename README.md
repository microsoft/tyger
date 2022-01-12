# Tyger: Signal Processing Control Plane

## TODO:
- Generate request ID, include in response header, logs
- Add run id as environment variable in run container so that it can be included in logs
- Init containers instead of crashing when dependencies are not ready?
- Test coverage
- Run on AKS
  - Managed Pod Identity
  - Azure storage
  - Managed PostgreSQL
  - Ingress TLS
  - Intra-cluster TLS
- Single PostgreSQL helm dependency with two databases (tyger and storage server)
- Persist buffer metadata in DB
- Persist run metadata in DB
- Support Pod [termination messages](https://kubernetes.io/docs/tasks/debug-application-cluster/determine-reason-pod-failure/).
- Support metadata tags for codespecs, runs, buffers?
- List/search runs
- List/search buffers
- Queryable buffer status
- Support computespec
- Delete endpoints
- RBAC
- Support mutliple clusters
- Support multiple storage accounts
