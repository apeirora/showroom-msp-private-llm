# Private LLM ORD publication

The existing portal-integration nginx publishes these static assets at:

- `/.well-known/open-resource-discovery`;
- `/ord/documents/*`;
- `/ord/definitions/*`.

The system-version document describes the concrete Chat Completions API. Its `compatibleWith`
declaration references the shared abstract contract published by Chat UI, which lets an ORD
consumer suggest this provider without matching titles or tags.

CI validates the configuration and document against
`@open-resource-discovery/specification@1.16.3` and validates the OpenAPI file. CORS is enabled
only on public ORD paths. SLA and cost labels are explicitly showroom demo metadata, not
measured Platform Mesh guarantees.

## Platform Mesh discovery contract

The `private-llm-pm-integration` chart advertises the configuration endpoint through this
experimental `ProviderMetadata` extension:

```yaml
spec:
  data:
    ord:
      configUrl: https://llm.example.com/.well-known/open-resource-discovery
```

`configUrl` is an absolute HTTPS URL for the ORD configuration endpoint. The provider owns the
URL and keeps the configuration, referenced documents, and definitions available and current.
The endpoint contains no credentials or tenant-specific information. The provider also adds the
same URL to `spec.documentation` with `displayName: ORD` for human discovery.

Consumers treat `spec.data` as an extensible object and ignore an absent or unknown `ord` field.
They fetch the configuration before its referenced documents and resolve URLs according to the
ORD specification. A missing, invalid, unsupported, or unavailable document must not block the
existing provider discovery or installation flow.

This contract is experimental. It can change after alignment with the Platform Mesh and ORD
teams. ORD remains descriptive metadata and does not control resource provisioning or lifecycle.
