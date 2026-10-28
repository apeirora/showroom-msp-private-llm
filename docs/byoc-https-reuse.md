# Reuse the existing HTTPS stack for BYOC

The 28 October direction for showroom#794 is to reuse Showroom's managed DNS,
certificate and Traefik setup. Customer-managed DNS and certificate renewal are
not prerequisites for the default implementation. The controller adapter below is implemented and covered by local tests.
Live development acceptance remains a separate release and migration gate.

Hosted Private LLM already uses `PUBLIC_SCHEME=https`, a `private-llm` TLS Secret,
Traefik and the `dns.gardener.cloud/*` and `cert.gardener.cloud/purpose=managed`
annotations. Its ingress is built in
`internal/controller/llminstance_controller.go`. The owning infrastructure values
are `apps/private-llm/overlays/dev/msp-values.yaml` in showroom-msp-cluster-infra.
That existing mechanism is the reference for issuance and renewal. Do not create
another ACME client or make customers arrange certificates for the default route.

The former BYOC implementation took a separate path in `llminstance_byoc.go`: it creates a remote
LoadBalancer forwarding public port 443 directly to llama.cpp's HTTP port 8000,
then published `http://<address>:443`. Merely reusing the MSP HTTPS ingress would
leave the public MSP-to-BYOC connection unencrypted.

## Implementation

1. Derive an instance hostname from the immutable instance UID and verified target cluster identity under
   an operator-configured Showroom domain. Do not use a tenant-editable slug or
   accept a tenant-supplied certificate subject. Inventory existing names and
   fail on foreign ownership before creating or changing any route.
2. Request a dedicated Gardener `Certificate` in an operator-controlled MSP
   credential namespace, using the existing managed issuer. Keep the Certificate
   and generated key Secret outside tenant API/related-Secret synchronization.
   Cross-namespace instance ownership requires explicit UID tracking and cleanup,
   not an invalid cross-namespace owner reference. Its SAN contains only that
   instance hostname. Use
   an explicit `DNSEntry` in the MSP to point that name to the remote LB address.
   The DNS/issuer policy and namespace RBAC must be verified for this managed
   subdomain; a working hosted certificate is not proof of every new DNS scope.
3. Deliver only this instance's TLS pair to an owned Secret on its BYOC cluster
   through the existing remote Kubernetes client, with API-server certificate
   and hostname verification required. Never deliver the shared
   wildcard key, ACME account key or DNS-provider credentials. Validate the
   key/certificate match, expected hostname and validity before delivery. Bind
   source and destination to the instance and remote target identities; refuse
   foreign pre-existing objects and never print/export key material.
4. Reuse Traefik as the remote TLS listener. For clusters without a shared
   ingress, run a minimal dedicated listener beside llama.cpp rather than install
   a cluster-wide ingress/DNS/certificate stack. Expose only HTTPS 443 to that
   listener; bind llama.cpp to loopback. Preserve BYOC's existing model-token
   checks and keep hosted-mode arguments/probes unchanged. Use a maintained,
   digest-pinned Traefik release; the infrastructure's `34.x` chart range is not
   an immutable runtime pin.
5. Reconcile certificate changes into the remote Secret and prove reload by
   observing the certificate served after replacement. Mount whole directories,
   not `subPath` files. A Secret update alone does not prove Traefik reloaded it. The delivered leaf
   fingerprint changes the pod template and causes a controlled Recreate rollout;
   the external probe must observe that exact fingerprint before Ready.
   A renewal error must not discard a still-valid serving certificate. Invalid
   or expired material must not produce a Ready HTTPS endpoint or HTTP fallback.
6. Publish the HTTPS URL in instance status and token Secrets only after a
   certificate-validating health check. The probe must verify hostname/SNI and
   reject redirects. Continue checking readiness after initial provisioning.
   Existing owned token Secrets use `https://unavailable.invalid` while the instance
   is unavailable; keys remain present so a transient certificate failure does not
   rotate model credentials. This avoids an empty URL selecting a default backend.
   Old HTTP URLs are removed from the provider Secret, but startup-cached clients
   still require coordinated migration and restart.

The additional work is the BYOC lifecycle adapter: per-instance certificate
delivery, the remote listener and renewal convergence. DNS issuance and TLS
handling reuse existing components. Do not claim that installing an annotation
on an arbitrary customer cluster will create these controllers there.

## Migration and verification

Stop affected development consumers before changing the endpoint, update their
URLs, rotate model API keys and restart them together. Removing HTTP on the server
does not encrypt a credential already sent by a stale client. Identify every
owned old port and route before removal; preserve customer-owned resources.
Reverting to the old plaintext controller is not a safe rollback.

Use an isolated fixture to prove HTTPS and streaming, rejection of plaintext and
invalid certificates, certificate renewal/reload, no public model port, updated
token URLs and unavailable-endpoint behavior. Then verify the same lifecycle on
the scoped development BYOC instance, including target changes and cleanup.
Hosted regression tests and instance ownership checks remain required. No live
change is authorized by this document.

References:

- [Gardener certificate issuance and renewal](https://gardener.cloud/docs/guides/networking/certificate-extension/)
- [Gardener explicit DNSEntry](https://gardener.cloud/docs/guides/networking/dns-extension/#creating-a-dnsentry-resource-explicitly)
- [Traefik dynamic certificates and strict SNI](https://doc.traefik.io/traefik/reference/routing-configuration/http/tls/tls-certificates/)

## Upgrade boundaries

`byocTLS.domain`, `namespace`, `issuerName`, `issuerNamespace` and `dnsClass` are
operator settings. Empty or invalid configuration does not enable plaintext.
The chart creates a dedicated credential namespace with namespaced Certificate
and DNSEntry permissions; it does not install Gardener controllers on BYOC.
Only public unicast IP LoadBalancers are currently supported. Hostname-only LBs
are rejected to avoid publishing a tenant-controlled CNAME that can rebind.

Legacy Deployment, Service and key Secret objects do not carry the new immutable
ownership annotation. They are deliberately refused until an operator verifies
and migrates the exact owned objects. A binary upgrade alone does not remove an
unreachable or unowned legacy plaintext listener. Inventory and stop consumers
and the old workload before this migration. The new operator withdraws stale URLs
for **every** unavailable instance it reconciles, so inventory all BYOC instances
on the target MSP even when only one remote instance is being piloted.

Target changes create a different TLS hostname and certificate, so a key retained
by a former cluster cannot impersonate the new endpoint. Model keys are also bound
to the target and instance identities and rotate during initial legacy migration
or a change of identity; routine TLS renewal retains model keys. Verify consumer
Secret synchronization and restart applications that cache keys or URLs.

Cleanup removes managed DNS and waits for its deletion before releasing the remote
LoadBalancer. It waits for Certificate and Secret deletion as well; an unreachable
old target keeps the finalizer and protected credentials for operator recovery.
The original domain/credential namespace and workload slug are persisted for
cleanup. Changing them in configuration requires explicit migration.
