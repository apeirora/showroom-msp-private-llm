// SPDX-License-Identifier: Apache-2.0
// Run the pinned production callers. Mock only framework and transport boundaries.
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, resolve, posix } from 'node:path';
import vm from 'node:vm';
import ts from 'typescript';
import * as rxjs from 'rxjs';
import * as operators from 'rxjs/operators';

export const revision = 'd49878f1c0de3f636615f99aeef1f1b63f68dce8';
const require = createRequire(import.meta.url);
const cache = resolve(import.meta.dirname, '.portal-source', revision);
export const tablePath = 'projects/wc/src/app/components/generic-ui/list-view/resource-table-card/resource-table-card.component.ts';
const servicePath = 'projects/lib/services/resource/resource.service.ts';
const gatewayPath = 'projects/lib/services/resource/gateway.service.ts';
const utilRoot = 'projects/lib/utils/utils/';
const utilNames = ['build-graphql-input-type-name', 'build-resource-list-operation', 'columns-to-gql-fields', 'flatten-field-tree', 'get-value-by-path', 'instance-permission-key', 'is-namespaced-resource', 'merge-list-with-subscription-result', 'resource-action-allowed', 'resource-sanitizer', 'resource-field-by-path'];
const detailPath = 'projects/wc/src/app/components/generic-ui/detail-view/detail-view.component.ts';
const paths = [detailPath, tablePath, servicePath, gatewayPath, ...utilNames.map(n => utilRoot + n + '.ts')];
const sources = new Map();
for (const path of paths) {
  const local = resolve(cache, path);
  let source;
  try { source = await readFile(local, 'utf8'); }
  catch {
    const response = await fetch(`https://raw.githubusercontent.com/platform-mesh/portal-ui-lib/${revision}/${path}`);
    if (!response.ok) throw new Error(`${path}: HTTP ${response.status}`);
    source = await response.text();
    await mkdir(dirname(local), { recursive: true });
    await writeFile(local, source);
  }
  sources.set(path, source);
}

export function callerHarness() {
  let context, search = { namespace: '-all-' }, effects = [], injections = new Map();
  const calls = [], modules = new Map();
  const signal = (value) => {
    const fn = () => value;
    fn.set = next => { value = next; };
    fn.update = update => { value = update(value); };
    return fn;
  };
  const token = name => ({ name });
  const LuigiCoreService = token('LuigiCoreService'), ApolloFactory = token('ApolloFactory');
  const ResourceService = token('ResourceService'), DestroyRef = token('DestroyRef');
  const ErrorHandlerService = token('ErrorHandlerService'), InstancePermissionsStore = token('InstancePermissionsStore');
  const angular = {
    Injectable: () => cls => cls, Component: () => cls => cls,
    inject: key => { if (!injections.has(key)) throw new Error(`Missing injection: ${key?.name}`); return injections.get(key); },
    signal, computed: fn => fn, untracked: fn => fn(),
    effect: fn => effects.push({fn, cleanups: []}),
    input: { required: () => () => context }, viewChild: { required: () => () => ({ closeCreateDialog(){}, closeDeleteDialog(){} }) },
    DestroyRef, ViewEncapsulation: {Emulated: 0}, ChangeDetectionStrategy: {OnPush: 0},
  };
  const core = { routing: () => ({ getSearchParams: () => search }), showAlert: () => {} };
  injections.set(LuigiCoreService, core);
  injections.set(DestroyRef, {});
  injections.set(ErrorHandlerService, {isUnauthorizedAccess: () => false, handleError: error => { throw error; }});
  injections.set(InstancePermissionsStore, {sync(){}});
  let utils;
  const external = name => {
    if (name === '@angular/core') return angular;
    if (name === '@angular/core/rxjs-interop') return {takeUntilDestroyed: () => value => value};
    if (name === '@openmfp/portal-ui-lib') return {LuigiCoreService};
    if (name === '@platform-mesh/portal-ui-lib/models') return {ALL_NAMESPACE: '-all-', kcpRootOrgsPath: 'root:orgs:', ResourceOperationTypeMap: {ADDED:'ADDED', MODIFIED:'MODIFIED', DELETED:'DELETED'}};
    if (name === '@platform-mesh/portal-ui-lib/utils') return utils;
    if (name === '@platform-mesh/portal-ui-lib/services') return {ResourceService, ErrorHandlerService};
    if (name === '@openmfp/ngx') return {DeclarativeTableCard: token('DeclarativeTableCard')};
    if (name === 'apollo-angular') return {gql: (parts, value) => value};
    if (name === 'rxjs') return rxjs;
    if (name === 'rxjs/operators') return operators;
    if (name === 'jsonpath-plus') return require(name);
    if (name === 'gql-query-builder') return require(name);
    throw new Error(`Unsupported import: ${name}`);
  };
  const load = path => {
    if (modules.has(path)) return modules.get(path);
    const source = sources.get(path);
    if (source === undefined) throw new Error(`Unpinned source: ${path}`);
    const output = ts.transpileModule(source, {compilerOptions: {target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, experimentalDecorators: true}}).outputText;
    const exports = {};
    modules.set(path, exports);
    const localRequire = name => {
      if (name.endsWith('instance-permissions-store.service')) return {InstancePermissionsStore};
      if (name.endsWith('create-resource-modal.consts')) return {ResourceFieldNames: {MetadataName: 'metadata.name', MetadataNamespace: 'metadata.namespace'}};
      if (name.endsWith('url-params')) return {addSearchParams: params => {search = {...search, ...params}; calls.push({kind: 'route', params});}};
      if (name.endsWith('to-form-fields')) return {toFormFields: fields => fields, expandCollectionEntries: value => value};
      if (name.endsWith('field-definition.utils')) return {};
      if (name === './apollo-factory') return {ApolloFactory};
      if (name.startsWith('.')) return load(posix.normalize(posix.join(posix.dirname(path), name + '.ts')));
      return external(name);
    };
    vm.runInThisContext(`(function(require,exports){${output}\n})`)(localRequire, exports);
    return exports;
  };
  utils = Object.assign({}, ...utilNames.map(name => load(utilRoot + name + '.ts')));
  const gateway = new (load(gatewayPath).GatewayService)();
  const apollo = (ctx, parent) => {
    const record = (kind, request) => {
      const subject = new rxjs.Subject();
      calls.push({kind, ...request, context: ctx, url: gateway.getGatewayUrl(ctx, parent), subject});
      return subject;
    };
    return {query: request => record('list', request), mutate: request => record('mutate', request), subscribe: request => record('watch', request)};
  };
  injections.set(ApolloFactory, {apollo});
  const service = new (load(servicePath).ResourceService)();
  injections.set(ResourceService, service);
  const table = new (load(tablePath).ResourceTableCard)();
  const luigi = {linkManager: () => ({navigate: name => calls.push({kind: 'navigate', name})}), uxManager: () => ({showAlert(){}})};
  table.LuigiClient = () => luigi;
  const flush = () => {
    for (const effect of effects) { effect.cleanups.forEach(fn => fn()); effect.cleanups = []; }
    for (const effect of effects) effect.fn(cleanup => effect.cleanups.push(cleanup));
  };
  return {table, service, gateway, calls, flush, detailDelete(resource, detailContext) {
    const ast = ts.createSourceFile('detail.ts', sources.get(detailPath), ts.ScriptTarget.ES2022, true);
    const declaration = ast.statements.find(node => ts.isClassDeclaration(node) && node.name.text === 'DetailView');
    const method = declaration.members.find(node => ts.isMethodDeclaration(node) && node.name.getText(ast) === 'delete');
    const body = ts.transpileModule('class DetailCaller {' + method.getText(ast) + '}', {compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;
    const caller = vm.runInThisContext('(function(){' + body + '; return new DetailCaller();})')();
    Object.assign(caller, {getResourceDefinition: () => detailContext.resourceDefinition, getResourceId: () => detailContext.resourceId, context: () => detailContext, resourceService: service});
    caller.delete(resource);
  }, refreshWatch() {
    const effect = effects[1]; effect.cleanups.forEach(fn => fn()); effect.cleanups = [];
    effect.fn(cleanup => effect.cleanups.push(cleanup));
  }, mount(node, namespace = '-all-', account = 'root:orgs:test:one') {
    if (node.entityType !== 'main.core_platform-mesh_io_account') throw new Error('Provider is absent from the account route');
    context = {...node.context, namespaceId: namespace === '-all-' ? undefined : namespace, kcpPath: account, token: 'test-token', portalContext: {crdGatewayApiUrl: 'https://gateway.test/clusters/root:orgs:stale/graphql'}};
    search = {namespace};
    flush();
    return context;
  }, context: () => context, search: () => search};
}
