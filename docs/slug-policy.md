# Operator-owned inference slugs

The manager requires `--slug-policy-file`. The same immutable policy controls
instance reconciliation, token publication and hosted authentication. Tenant
annotations and status are not naming authority. The old slug annotation may
remain present or change through synchronization; it is ignored. Use
`status.slug` for the operator-selected result. New identities derive a slug
from the service-cluster LLMInstance UID; existing identities need an explicit
reviewed entry if they predate the fixed cutover.

```json
{
  "cutover": "2026-10-28T00:00:00Z",
  "entries": [
    {
      "uid": "REPLACE_WITH_VERIFIED_MSP_INSTANCE_UID",
      "namespace": "VERIFIED_MSP_NAMESPACE",
      "name": "VERIFIED_INSTANCE_NAME",
      "mode": "legacy",
      "slug": "VERIFIED_EXISTING_SLUG"
    }
  ]
}
```

This example is not a deployable migration map. Fix the cutoff from the selected
API server's clock after stopping resource creation and collecting the inventory.
Never template the cutoff from the current time on every install. Map the MSP
UID, namespace and name, not the different source KCP UID. New derived names start
with `llm-`; legacy entries cannot occupy or prefix that range. Duplicate or
overlapping legacy names are rejected. Missing or mismatched mappings reject reconciliation and token publication;
hosted auth denies access and the owned route is withdrawn. An already-running
BYOC workload can keep serving its existing key on its established HTTPS endpoint.
This preserves remote ownership and cleanup credentials; it is not remote
revocation. Map every existing BYOC instance before rollout.

## Preserve existing endpoints

Slug ownership does not migrate synchronized resources. Updating the sync-agent
namespace template does not relocate existing mirrors; related Secrets follow
their retained primary namespace. A new token request can therefore land apart
from a legacy instance and fail to resolve it. More seriously, a retained request
can resolve an instance from another source namespace if both still share one MSP
namespace: the token controller resolves instance names within the MSP namespace.
Its UID and owner checks do not establish the original KCP namespace relationship.

Before approving any legacy entry, prove that every retained MSP namespace contains
LLMInstances, APITokenRequests and related Secret replicas from exactly one trusted
KCP logical-cluster/source-namespace tuple, with no competing provider credential
writers. Verify every request resolves to its intended source instance. UID/route
ownership alone is insufficient. If the relationship is mixed, ambiguous or
unprovable, withhold the mapping, withdraw only the affected owned hosted route,
and migrate the source instances and requests together through normal owner cleanup
and recreation in the isolated namespace before restoring access. Do not force
finalizers or delete a shared namespace. Missing mappings do not revoke existing
BYOC keys; preserve their cleanup coordinates and handle those targets separately.

1. Verify the selected cluster and every existing LLMInstance's MSP/source
   identity, owned route, token request and published endpoint. For BYOC, also
   record the active target, active slug and protected cleanup coordinates.
   Tenant-supplied annotations alone cannot prove legitimate ownership. Resolve
   disputed identities before approving a legacy entry. Inventory all shared
   ingress routes: a global default backend or an overlapping parent/catch-all
   path is conservatively treated as a conflict, even when it previously worked
   through controller-specific priority. Resolve that routing contract before
   rollout; do not assume transparent compatibility or disable the guard.
2. Put each established identity in the reviewed `legacy` map. Keep its existing
   slug exactly. Use `mode: derive` only for an explicitly planned endpoint
   migration; omit `slug`. Do not use derive to rename an active BYOC workload.
   A mismatch with the active BYOC slug is rejected before workload/cache writes.
3. Stop competing controllers and freeze new resource creation for the cutover.
   Install the updated CRD/status schema and one manager using the same policy for
   controllers and auth. Both supplied deployment paths use `Recreate`; there must
   be no second old manager serving authentication or reconciling annotations.
   Hosted auth can be unavailable during this transition. Do not promise seamless
   service or change the chart back to rolling updates for this migration.
4. Verify current generations, canonical status slugs, exact middleware bindings,
   unchanged approved legacy URLs, token synchronization and real request results.
   Test cross-account keys, tenant annotation changes, prefix conflicts and
   recreated identities using disposable resources. Fixture success is not live
   consumer acceptance.
5. Remove test requests, mirrored Secrets and workloads with UID preconditions.
   Check both KCP and MSP copies; namespace disappearance alone is not enough.

Changing a legacy entry later is a separate migration. Keep the reviewed map
stable, for any continuing instance. Deletion retains the persisted active cleanup
coordinates and protected cached credentials even if its policy entry is absent.
Older BYOC instances without a captured active slug still need their reviewed
legacy map for deletion; the tenant annotation is not trusted as a fallback. If a rollout fails, withhold the
affected routes or stop consumers; returning to an old binary restores annotation
authority and is not a security rollback. Unreachable historical BYOC targets
need their own recovery plan; do not adopt or delete them through this migration.

## Installation paths

- Standalone Helm: set `slugPolicy.cutover` and `slugPolicy.entries` in the
  reviewed values file. The chart mounts `private-llm-slug-policy` read-only and
  rolls the manager when its content changes.
- MSP umbrella: use `private-llm-operator.slugPolicy`.
- OCM installer: use `operator.slugPolicy`; the FluxDeployer passes it to the
  extracted operator chart. Verify the served HelmRelease and manager mount.
- Kustomize / `make deploy`: replace `config/default/slug-policy.json` with the
  reviewed map before building. Its generated ConfigMap hash changes the Pod
  template. The shipped empty cutoff intentionally cannot start the manager.
- Host development: `make run SLUG_POLICY_FILE=/protected/path/policy.json`.
  An empty-instance local installation can use a fixed API-clock cutoff and an
  empty entries array, then create its first instance after that cutoff.

The policy contains identity metadata, not credentials. Restrict write access to
the deployment owner; do not source it from tenant-controlled resources.
