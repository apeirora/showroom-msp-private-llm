// SPDX-License-Identifier: Apache-2.0
// Load the exact table-card caller, not a copy of its column/action logic.
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {gunzipSync} from 'node:zlib';
import ts from 'typescript';
import vm from 'node:vm';
import * as rxjs from 'rxjs';
const path = new URL('.portal-source/ngx-1.5.0.tgz', import.meta.url);
let archive;
try { archive = await readFile(path); }
catch {
  const response = await fetch('https://registry.npmjs.org/@openmfp/ngx/-/ngx-1.5.0.tgz');
  if (!response.ok) throw new Error(`ngx: HTTP ${response.status}`);
  archive = Buffer.from(await response.arrayBuffer());
  await mkdir(new URL('.portal-source/', import.meta.url), {recursive:true});
  await writeFile(path, archive);
}
if (createHash('sha512').update(archive).digest('base64') !== '/02npoqty8M9URn1Dd/I9dVF+g0ArGxie3iCW0MTUYTDS4ZygoEAnJfPkY2XaR1ZCc2Qka7XpxoD2ZS9BVG7wQ==') throw new Error('ngx integrity mismatch');
const tar = gunzipSync(archive);
let source;
for (let offset = 0; offset < tar.length; ) {
  const name = tar.subarray(offset, offset+100).toString().replace(/\0.*$/s,'');
  const size = parseInt(tar.subarray(offset+124,offset+136).toString().replace(/\0.*$/s,''),8) || 0;
  if (name === 'package/fesm2022/openmfp-ngx.mjs') source = tar.subarray(offset+512,offset+512+size).toString();
  offset += 512 + Math.ceil(size/512)*512;
}
if (!source) throw new Error('Missing ngx production caller');
const ast = ts.createSourceFile('ngx.js', source, ts.ScriptTarget.ES2022, true, ts.ScriptKind.JS);
const declaration = ast.statements.find(node => ts.isClassDeclaration(node) && node.name?.text === 'DeclarativeTableCard');
// Angular's generated static component metadata is not needed by a caller unit test.
const members = declaration.members.filter(node => !node.modifiers?.some(modifier => modifier.kind === ts.SyntaxKind.StaticKeyword));
const classSource = 'class DeclarativeTableCard {\n' + members.map(node => node.getText(ast)).join('\n') + '\n}';

export function ngxTableCard(config) {
  const signal = value => { const fn = () => value; fn.set = v => {value=v;}; return fn; };
  const input = value => signal(value);
  const output = () => ({values:[], emit(value){this.values.push(value);}});
  class FormControl {valueChanges = rxjs.EMPTY; constructor(value){this.value=value;} setValue(value){this.value=value;}}
  const names = ['input','computed','signal','output','FormControl','ngDevMode','debounceTime','distinctUntilChanged','takeUntilDestroyed','toObservable','map','filter'];
  const values = [input, fn => fn, signal, output, FormControl, false, rxjs.debounceTime, rxjs.distinctUntilChanged, () => value => value, fn => rxjs.of(fn()), rxjs.map, rxjs.filter];
  const card = vm.runInThisContext(`(function(${names.join(',')}){${classSource}; return new DeclarativeTableCard();})`)(...values);
  card.config.set(config);
  return card;
}
