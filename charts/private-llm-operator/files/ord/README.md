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
