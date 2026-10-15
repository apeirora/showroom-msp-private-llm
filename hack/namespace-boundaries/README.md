# Native namespace checks

Run `python3 hack/namespace-boundaries/check.py --chart charts/private-llm-sync-agent`.
Install PyYAML 6.0.3, Helm and Go 1.26.4 first. Build the chart dependencies.

The runner reads actual rendered PublishedResources.
It uses an overlay on the original api-syncagent v0.7.0 source at d9e14d998a374d81fb73254069b9be0f3d02cd9f.
It does not change that source. `MSP_SYNCAGENT_SOURCE` can supply a local Git checkout of that revision.

The tests check same-name objects in two KCP namespaces, adoption coordinates, related Secret names, long inputs and annotation-based reverse routing.
Object names stay unchanged. Runtime namespaces include the account and source namespace.
The complete Group A artifact set also tests the native LLM token status and Secret copy through KCP into a separate Chat UI replica.
State persistence uses a fixture because this test checks namespace identity, not permissions.

Namespace changes do not migrate existing workloads. Review any existing runtime objects before an upgrade.
