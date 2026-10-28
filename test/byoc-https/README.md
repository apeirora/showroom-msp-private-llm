# BYOC HTTPS listener fixture

This fixture runs the pinned Traefik listener beside a dummy model in a shared
Docker network namespace. The model listens only on loopback. It does not use a
cluster, Gardener, a model download, or real credentials.

Requirements: Go from this repository's `go.mod`, Docker, and permission to use
the selected Docker context. Run from the repository root:

```sh
CGO_ENABLED=0 GOFLAGS=-buildvcs=false go run ./test/byoc-https --report /tmp/byoc-https-report.json
```

`CGO_ENABLED=0` builds the executable for use inside the container as well as on
the host. Docker publishes an automatically selected **loopback-only** port. The
fixture creates uniquely named containers and a bridge network, then removes
only those resources. Certificate private keys remain in a mode-0700 temporary
directory outside the checkout and are deleted on normal completion or error.
If the process is forcibly killed, remove only its `byoc-https-<pid>` and
`byoc-https-<pid>-model` containers, its same-named network, and its protected
`/tmp/byoc-https-*` directory after checking ownership.

The JSON report records these checks:

- Trusted HTTPS with CA and hostname verification; missing/wrong/valid model key.
- First streaming chunk before completion, measured over the TLS connection.
- Plaintext HTTP rejected without a redirect to HTTPS.
- Untrusted certificates, missing SNI, unknown SNI, expired certificates and
  certificates for another hostname rejected by a verifying client.
- A sibling container can reach Traefik but cannot reach the loopback model port.
- Kubernetes-style atomic `..data` symlink replacement followed by a controlled
  restart serves the replacement certificate, checked by its fingerprint.

The fixture records whether a symlink replacement alone reloads the certificate.
This is an observation, **not a required or relied-on reload mechanism**. The
controller must change a pod-template certificate revision so a new pod starts
with the renewed Secret. The fixture restarts both containers, and refreshes its
host port because Docker may allocate another port on restart.

The fixture uses a private throwaway CA and a deliberately trivial dummy token
check. It verifies the TLS and proxy behavior only. It does not prove Gardener
issuance or renewal, controller ownership, kubelet Secret synchronization,
llama.cpp authorization, client migration, key rotation, or live availability.

The pinned image is Traefik `v3.7.16`, release 20 October 2026, at the multi-platform
registry index digest `sha256:1336e85aaed293bd63d005086e0dd013b04078b26aaeb1c18b26a2f928af7e65`.
The release/support evidence was checked on 28 October 2026:

- <https://github.com/traefik/traefik/releases/tag/v3.7.16>
- <https://doc.traefik.io/traefik/deprecation/releases/>
- <https://doc.traefik.io/traefik/v3.7/reference/routing-configuration/http/tls/tls-options/>
- <https://doc.traefik.io/traefik/v3.7/reference/routing-configuration/http/load-balancing/service/>

When the controller listener configuration or image changes, update this fixture
with it and rerun it. This standalone configuration is intentionally visible for
comparison; passing this fixture alone does not establish controller parity.
