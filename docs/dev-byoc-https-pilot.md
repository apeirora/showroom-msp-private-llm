# Development BYOC HTTPS pilot

## WHAT

The provider runs on development `msp02`. The selected remote BYOC pilot target
is the development `cc-d2` shoot in project `pm-cc-d2`; keep the pilot workload
separate from the platform services already running there.
This is an execution checklist, not evidence that a release or migration occurred.
The local fixture is in `test/byoc-https`; controller tests and a live pilot are
separate gates. Production remains outside this pilot.

## WHY

The former BYOC Service exposed plaintext on port 443. Clients may retain that
HTTP URL and API key even after the server becomes HTTPS-only. Stop consumers,
change the route and URLs, rotate keys, and restart together. Returning to the
plaintext controller is not a rollback option.

At infrastructure commit `0358e37d9ed29f48f391ed61978e22dda592e09d`:

- The development MSP chart is pinned to `2.17.1-rc.1`.
- The production overlay pins both MSP and PM charts to `2.16.1-rc`, overriding
  the base range. Production does not follow that base range.
- The prerelease guard in `hack/release-image-tags.sh` keeps RC publication from
  changing the stable controller image's `:latest` tag.

Use a reviewed **prerelease** version and an exact development chart/image pin.
Do not publish a stable release for this pilot. Do not modify production pins or
assume the absence of a checked-in `:latest` reference proves there is no live
image automation. Recheck the live sources and HelmRelease values before release.

## ACTION

1. **Review and release.** Run controller/hosted regression tests, the TLS fixture,
   adversarial review, and release-tag tests. Resolve blockers on the exact commit.
   Follow the repository tag workflow using a unique prerelease tag. Record the
   tag commit, matching workflow run, image digest, chart package digests and OCM
   version. Confirm the release is marked prerelease and `build-metadata.json`
   reports `image.latest=false`. No ad-hoc pushed image is needed.

2. **Prepare development configuration.** In an isolated infrastructure worktree,
   change only the `cc-d2/msp02` MSP chart pin and development values. Set the
   operator image tag explicitly to the reviewed prerelease (or use a supported
   digest pin): `helm package --app-version` alone does not update the embedded
   `values.image.tag`. Pull the published umbrella chart and inspect its rendered
   Deployment, permissions, CRDs and every relevant image before rollout.

   Configure the operator's managed BYOC domain, issuer and protected credential
   namespace through the implemented chart interface. A candidate development
   subdomain is `byoc.private-llm.msp02.dev.showroom.apeirora.eu`; verify the exact
   runtime configuration and ownership before using it. Reuse issuer
   `kube-system/traefik-cert-issuer`. Source configuration currently allows
   `dev.showroom.apeirora.eu` and `*.dev.showroom.apeirora.eu`. The existing
   `default/ccloud-dns` DNSProvider includes `showroom.apeirora.eu`, and the shoot
   template enables `shoot-dns-service` and shoot certificate issuers. These are
   source facts, not proof of live readiness or authorization from the new
   credential namespace. Verify the live issuer, DNS controller class, namespace
   permissions, and the per-instance Certificate/DNSEntry reconciliation.

3. **Identify and stop pilot consumers.** Re-read the target LLMInstance UID,
   namespace, slug, cluster reference, API-server identity and remote `kube-system`
   UID. Historical candidate `26jcf4ww8bl4l42q/demo-llm-on-simple-cluster` must not
   be used without this fresh verification. Inventory APITokenRequests and all
   synchronized Secret copies by metadata; locate Chat UI/direct-client consumers
   and their owning reconciler. Record their restart method and original scale.
   Inventory all BYOC instances on msp02: the controller upgrade withdraws unsafe
   endpoints across its scope even when only one remote instance is piloted.
   Stop the pilot consumers and legacy model Deployment before changing endpoints or rotating keys. An
   inventory gap blocks the migration, not the local implementation.

4. **Migrate owned resources.** The new controller refuses unowned or legacy
   objects. Match existing Deployment, Service and key Secret to the exact pilot
   using current operator labels, selectors, pod spec, source identity and cached
   credentials before applying any narrowly scoped ownership migration. Snapshot
   metadata and specs without Secret values. Preserve customer resources and other
   instances. Remove every owned plaintext port/route before certificate issuance;
   do not retain HTTP while waiting. The public Service must expose only 443 to
   the TLS listener, and the model must bind loopback.

5. **Verify issuance and rotate credentials.** Wait for the dedicated source
   certificate and owned remote TLS Secret, then observe the expected certificate
   fingerprint and hostname on the endpoint with normal CA verification. Verify
   Ready only after the current Deployment generation and TLS check succeed.
   Update all pilot client URLs to the verified HTTPS endpoint. The owned-token
   controller rotates keys when it first binds the legacy token to the verified
   target/instance identity. Verify that rotation and the key Secret revision cause a
   rollout, and verify old keys fail. Record only revisions and result codes.
   Confirm every synchronized copy has the HTTPS URL before restarting consumers.

6. **Accept at the consuming layer.** Prove valid-key inference and streaming,
   missing/wrong/old-key rejection, wrong hostname/unknown CA rejection, plaintext
   HTTP rejection, and no public model port. Reissue or replace the dedicated
   instance certificate through the supported certificate lifecycle, observe
   remote propagation and pod revision change, then compare the served fingerprint.
   A Secret update alone does not prove reload; the fixture did not observe reload
   from a `..data` symlink swap alone. Check renewal failure retains a valid serving
   certificate, while expired/invalid material cannot produce a Ready endpoint.
   Verify target-change and deletion cleanup on a disposable instance if doing so
   would disrupt the pilot. Confirm hosted workloads and production versions
   remained unchanged. Keep exact remaining evidence gaps open on the ticket.

   For a manual renewal, first verify the dedicated Certificate's UID and instance
   ownership label. Set `spec.renew: true` and clear `spec.ensureRenewedAfter` to
   `null` on that Certificate only. Gardener acknowledges the request by removing
   `renew` and setting `ensureRenewedAfter`; the operator preserves those two
   controls while continuing to enforce the hostname, issuer and output Secret.
   Wait for the new certificate fingerprint, remote Secret propagation and Ready
   pod revision before accepting the renewal. Model API keys must remain unchanged.
   Do not delete the Certificate or its output/cache Secrets to force renewal, and
   do not revoke a valid certificate as a renewal test.

If a gate fails, leave affected consumers stopped and the endpoint unavailable.
Keep the secure listener/configuration and investigate the failure; never restore
the old plaintext Service or point clients at HTTP. Preserve the dedicated TLS
Secret while it remains valid. Resume only after the same acceptance checks pass.
