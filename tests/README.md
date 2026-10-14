# Provider account caller tests

Run `npm ci --ignore-scripts` and `npm test` in this directory.
The tests require Helm and Node.js 24.

The suite renders the chart and executes the portal callers at commit
`d49878f1c0de3f636615f99aeef1f1b63f68dce8`.
It also executes the table card from `@openmfp/ngx` 1.5.0 with its SHA-512 integrity check.
The test cache contains public source only.

The five tests cover the account list, namespace selection and creation,
resource identity and detail routes, detail delete, and account changes.
Set `PROVIDER_SOURCE` to an old chart checkout to run the same suite against that source.
All five tests must fail on the old namespace attachment.

Angular dependency injection, signal scheduling, Luigi, and Apollo transport use test doubles.
RxJS, the GraphQL query builder, and the production caller methods execute directly.
The tests do not prove browser component mount, reload, Back, or dialog disposal.
Live acceptance remains required after provider activation.

The generic form requires `metadata.namespace` when the selector shows all namespaces.
The list has no delete action.
Delete uses the detail route's namespace.
A separate test records the direct list delete defect in the pinned portal service.
It expects that call to omit the row namespace and fails if the upstream behavior changes.

Refs apeirora/showroom#583
