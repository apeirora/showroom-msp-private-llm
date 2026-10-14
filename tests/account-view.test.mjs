// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import {resolve} from 'node:path';
import {parseAllDocuments} from 'yaml';
import {callerHarness, revision} from './portal-caller.mjs';
import {ngxTableCard} from './ngx-caller.mjs';

const repo = resolve(process.env.PROVIDER_SOURCE || resolve(import.meta.dirname, '..'));
const config = JSON.parse(readFileSync(new URL('./provider.json', import.meta.url)));
const rendered = execFileSync('helm', ['template', 'caller-test', resolve(repo, config.chart), ...(config.args || []), ...(process.env.PROVIDER_HELM_SET ? ['--set', process.env.PROVIDER_HELM_SET] : [])], {encoding:'utf8'});
const cm = parseAllDocuments(rendered).map(doc => doc.toJSON()).find(doc => doc?.kind === 'ConfigMap' && doc.data?.['pm-content.json']);
assert.ok(cm, 'Helm must serve pm-content.json');
const nodes = JSON.parse(cm.data['pm-content.json']).luigiConfigFragment.data.nodes.filter(n => n.url?.endsWith('#generic-list-view'));
assert.equal(nodes.length, config.lists);
const row = (namespace, name = 'same') => ({metadata:{name, namespace}, spec:{}, status:{}});
function response(node, rows) {
  const rd = node.context.resourceDefinition;
  const list = {items: rows, resourceVersion:'1'};
  return {data: rd.apiGroup ? {[rd.apiGroup]:{[rd.version]:{[rd.entityCollection]:list}}} : {[rd.version]:{[rd.entityCollection]:list}}};
}
const last = (h, kind) => h.calls.filter(c => c.kind === kind).at(-1);

// Each test enters through the rendered account attachment, so all five reject old source.
test(`account attachment lists every namespace through pinned portal ${revision}`, () => {
  for (const node of nodes) {
    const h = callerHarness(); h.mount(node);
    const rd = node.context.resourceDefinition;
    assert.equal(rd.scope, 'Namespaced');
    assert.ok(rd.entity && rd.entityCollection && rd.version);
    assert.ok(rd.ui.listView.fields.some(f => f.property === 'metadata.namespace'));
    assert.equal(node.webcomponent.type, 'module');
    const request = last(h,'list');
    assert.equal(request.variables.namespace, undefined);
    assert.ok(request.query.includes(rd.entityCollection));
    assert.equal(request.url, 'https://gateway.test/clusters/root:orgs:test:one/graphql');
    request.subject.next(response(node, [row('alpha'), row('beta')]));
    assert.equal(h.table.resources().length, 2);
  }
});

test('native namespace change scopes the list and requires one namespace for creation', () => {
  for (const node of nodes) {
    const h = callerHarness(); h.mount(node);
    if (node.context.resourceDefinition.ui.createView?.fields?.length) {
      const namespace = h.table.createFormFields().find(f => f.property === 'metadata.namespace');
      assert.equal(namespace?.required, true);
      h.service.create(row('chosen'), node.context.resourceDefinition, h.context()).subscribe();
      assert.equal(last(h, 'mutate').variables.namespace, 'chosen');
    }
    h.mount(node, 'alpha');
    assert.equal(last(h, 'list').variables.namespace, 'alpha');
    assert.equal(h.context().namespaceId, 'alpha');
  }
});

test('same names in different namespaces keep separate identities and hidden detail routes', () => {
  for (const node of nodes) {
    const h = callerHarness(); h.mount(node);
    last(h,'list').subject.next(response(node,[row('alpha'),row('beta')]));
    const resources = h.table.tableResources();
    assert.notEqual(resources[0].id, resources[1].id);
    // Generated secret lists deliberately have no edit/detail route.
    if (!node.context.resourceDefinition.ui.detailView) continue;
    const child = node.children.find(child => child.pathSegment.startsWith(':'));
    assert.equal(child.hideFromNav,true);
    assert.equal(child.context.resourceId,child.pathSegment);
    assert.equal(child.url, '/assets/platform-mesh-portal-ui-wc.js#generic-detail-view');
    assert.equal(child.webcomponent.type,'module');
    h.table.navigateToResource(resources[1]);
    assert.equal(h.search().namespace,'beta');
    assert.equal(last(h,'navigate').name,'same');
    h.service.read('same', {...node.context.resourceDefinition, entity:node.context.resourceDefinition.entity}, ['metadata'], {...h.context(), namespaceId:'beta'},false).subscribe();
    assert.equal(last(h,'list').variables.namespace,'beta');
  }
});

test('the list has no delete control and detail delete uses the row namespace', () => {
  for (const node of nodes) {
    const h = callerHarness(); h.mount(node);
    const card = ngxTableCard(h.table.config());
    const fields = card.effectiveColumns();
    assert.ok(fields.length);
    assert.ok(fields.every(field => field.uiSettings?.buttonSettings?.action !== 'delete'));
    // Without a delete column there is no button event that opens the delete dialog.
    for (const field of fields) card.onButtonClick({field, resource:row('beta')});
    assert.equal(card.deleteDialogOpen(), false);
    if (!node.context.resourceDefinition.ui.detailView) continue;
    h.table.navigateToResource(row('beta'));
    h.detailDelete(row('beta'), {...h.context(), namespaceId:h.search().namespace, resourceId:'same'});
    const request = last(h,'mutate');
    assert.equal(request.variables.namespace,'beta');
    assert.equal(request.variables.name,'same');
    assert.equal(request.url,'https://gateway.test/clusters/root:orgs:test:one/graphql');
  }
});

test('account switch cancels old list and watch responses and uses context.kcpPath', () => {
  for (const node of nodes) {
    const h = callerHarness(); h.mount(node);
    const old = last(h,'list');
    old.subject.next(response(node,[row('alpha','old')]));
    h.refreshWatch();
    const oldWatch = last(h,'watch');
    h.mount(node,'-all-','root:orgs:test:two');
    assert.equal(old.subject.observed,false);
    assert.equal(h.table.resources().length,0);
    assert.equal(oldWatch.subject.observed, false);
    oldWatch.subject.next({data: {}});
    old.subject.next(response(node,[row('alpha','late')]));
    assert.equal(h.table.resources().length,0);
    const current = last(h,'list');
    assert.equal(current.url,'https://gateway.test/clusters/root:orgs:test:two/graphql');
    current.subject.next(response(node,[row('beta','current')]));
    assert.equal(h.table.resources()[0].metadata.name,'current');

  }
});

// Expected failure: portal-ui-lib d49878f1 ResourceService.delete, lines 372 to 388,
// calls getNamespace(nodeContext) without the row. This test fails if upstream fixes it.
test('known upstream gap: direct list delete omits the row namespace (expected failure)', () => {
  for (const node of nodes) {
    const h = callerHarness(); h.mount(node);
    h.table.delete(row('beta'));
    assert.throws(() => assert.equal(last(h,'mutate').variables.namespace,'beta'), {name:'AssertionError'});
  }
});


test('core Secrets use v1.Secrets for legacy and explicit empty API group values', () => {
  const configurations = [
    ['portalIntegration.secretResourceDefinition.group=v1', 'portalIntegration.secretResourceDefinition.version='],
    ['portalIntegration.secretResourceDefinition.group=', 'portalIntegration.secretResourceDefinition.version=v1'],
  ];
  for (const values of configurations) {
    const args = ['template','caller-test',resolve(repo,config.chart),...config.args];
    if (process.env.PROVIDER_HELM_SET) args.push('--set',process.env.PROVIDER_HELM_SET);
    for (const value of values) args.push('--set-string',value);
    const documents = parseAllDocuments(execFileSync('helm',args,{encoding:'utf8'})).map(doc => doc.toJSON());
    const content = documents.find(doc => doc?.kind === 'ConfigMap' && doc.data?.['pm-content.json']);
    const secret = JSON.parse(content.data['pm-content.json']).luigiConfigFragment.data.nodes.find(node => node.label === 'Secrets');
    const h = callerHarness(); h.mount(secret);
    assert.equal(secret.context.resourceDefinition.apiGroup,'');
    assert.equal(secret.context.resourceDefinition.version,'v1');
    const request = last(h,'list');
    assert.equal((request.query.match(/\bv1\s*\{/g) || []).length,1);
    assert.ok(request.query.includes('Secrets'));
    assert.equal(request.variables.namespace,undefined);
  }
});
