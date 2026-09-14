#!/usr/bin/env bun
// Generates web/src/api/generated.ts from api/openapi.yaml: one interface per
// component schema, typed path builders, and thin per-operation fetch wrappers
// that delegate to apiClient (src/api/client.ts) so generated calls share the
// hand-written client's base URL, auth header, and error handling exactly.
//
// The spec is hand-written and drift-pinned by TestOpenAPIMatchesRoutes; this
// script is the only place that turns it into TypeScript. Run from web/:
//
//   bun scripts/gen-client.mjs
//
// The output is committed (repo convention: generated code lands in git, CI
// does not regenerate it) — review it like any other code change.
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { parse } from 'yaml';

const specPath = fileURLToPath(new URL('../../api/openapi.yaml', import.meta.url));
const outPath = fileURLToPath(new URL('../src/api/generated.ts', import.meta.url));

const spec = parse(readFileSync(specPath, 'utf8'));
if (typeof spec?.openapi !== 'string' || !spec.openapi.startsWith('3.1.')) {
  throw new Error(`api/openapi.yaml is not an OpenAPI 3.1 document (got ${spec?.openapi})`);
}

// --- schema -> TypeScript ----------------------------------------------------

const HTTP_METHODS = ['get', 'put', 'post', 'delete', 'options', 'head', 'patch', 'trace'];

const refName = (ref) => {
  const m = /^#\/components\/schemas\/(.+)$/.exec(ref);
  if (!m) throw new Error(`unsupported $ref (want #/components/schemas/...): ${ref}`);
  return m[1];
};

/** A schema's TypeScript type; nested objects come back inline. */
function leafType(schema) {
  if (schema == null || (typeof schema === 'object' && !Array.isArray(schema) && Object.keys(schema).length === 0)) {
    return 'unknown';
  }
  if (typeof schema.$ref === 'string') return refName(schema.$ref);
  const variants = schema.oneOf ?? schema.anyOf;
  if (variants) return variants.map(leafType).join(' | ');
  if (schema.enum) return schema.enum.map((v) => (typeof v === 'string' ? JSON.stringify(v) : String(v))).join(' | ');
  const base = (t) => {
    switch (t) {
      case 'string': return 'string';
      case 'integer':
      case 'number': return 'number';
      case 'boolean': return 'boolean';
      case 'null': return 'null';
      case 'array': return `${leafType(schema.items ?? {})}[]`;
      case 'object': return objectType(schema);
      default: throw new Error(`unsupported schema type ${JSON.stringify(t)}`);
    }
  };
  const types = Array.isArray(schema.type) ? schema.type.map(base) : [base(schema.type)];
  return [...new Set(types)].join(' | ');
}

/** Inline object type: { a: string; b?: number } — or Record when unshaped. */
function objectType(schema) {
  const props = schema.properties ?? {};
  const keys = Object.keys(props);
  if (keys.length === 0) {
    const extra = schema.additionalProperties;
    if (extra && typeof extra === 'object') return `Record<string, ${leafType(extra)}>`;
    return 'Record<string, unknown>';
  }
  const required = new Set(schema.required ?? []);
  const fields = keys.map((name) => {
    const key = /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(name) ? name : JSON.stringify(name);
    return `${key}${required.has(name) ? '' : '?'}: ${leafType(props[name])}`;
  });
  const extra = schema.additionalProperties;
  if (extra) {
    const t = typeof extra === 'object' ? leafType(extra) : 'unknown';
    fields.push(`[key: string]: ${t}`);
  }
  return `{ ${fields.join('; ')} }`;
}

// --- operation naming --------------------------------------------------------

const pascal = (part) =>
  part
    .replace(/[{}]/g, '')
    .split(/[^A-Za-z0-9]+/)
    .filter(Boolean)
    .map((w) => w[0].toUpperCase() + w.slice(1))
    .join('');

const camel = (part) => {
  const p = pascal(part);
  return p[0].toLowerCase() + p.slice(1);
};

/** GET /api/executions/{execution_id}/reports -> getExecutionsByExecutionIdReports */
function operationName(method, path) {
  const segments = path.replace(/^\/api/, '').split('/').filter(Boolean);
  const name = segments.map((s) => (s.startsWith('{') ? `By${pascal(s)}` : pascal(s))).join('');
  return `${method.toLowerCase()}${name}`;
}

// --- parameters / bodies / responses -----------------------------------------

const components = spec.components ?? {};
const resolveParam = (p) =>
  typeof p.$ref === 'string' ? components.parameters[refNameLoose(p.$ref)] : p;
const refNameLoose = (ref) => /^#\/components\/parameters\/(.+)$/.exec(ref)?.[1] ?? '';

function opModel(path, method, op, pathItemParams) {
  const params = [...(pathItemParams ?? []), ...(op.parameters ?? [])].map(resolveParam);
  const pathParams = params.filter((p) => p?.in === 'path');
  const queryParams = params.filter((p) => p?.in === 'query');

  const body = op.requestBody?.content ?? {};
  const [contentType, bodySpec] = Object.entries(body)[0] ?? [];

  let response = 'void';
  for (const code of ['200', '201', '202', '203', '206', 'default']) {
    const res = op.responses?.[code];
    if (!res) continue;
    const content = res.content ?? {};
    if (content['application/json']) response = leafType(content['application/json'].schema);
    else if (content['text/plain']) response = 'string';
    break;
  }

  return {
    name: operationName(method, path),
    path,
    pathParams,
    queryParams,
    contentType,
    bodyType: contentType ? leafType(bodySpec?.schema ?? {}) : null,
    response,
    summary: (op.summary ?? '').replace(/\s+/g, ' ').trim(),
  };
}

/** Query values cross the wire as strings; allow the natural JS spellings. */
const queryValueType = (schema) => {
  const t = leafType(schema);
  switch (t) {
    case 'number': return 'number | string';
    case 'string': return 'string';
    case 'boolean': return 'boolean';
    default: return t.endsWith('[]') ? `Array<${t.slice(0, -2)}>` : t;
  }
};

const httpMethodOf = (name) =>
  ['get', 'post', 'put', 'delete', 'patch', 'options', 'head'].find((m) => name.startsWith(m)) ?? 'get';

// --- code emission -----------------------------------------------------------

const out = [];
out.push(`// Generated by web/scripts/gen-client.mjs from api/openapi.yaml — DO NOT EDIT BY HAND.
// Regenerate with: cd web && bun scripts/gen-client.mjs (then commit the output;
// CI does not regenerate). Types mirror the spec's schemas; every wrapper goes
// through apiClient, so generated calls share the hand-written client's base
// URL ("/api"), bearer-token header, and ApiError error handling.

import { apiClient } from './client';

`);

// Component schemas first: everything else references these names.
const schemas = components.schemas ?? {};
for (const [name, schema] of Object.entries(schemas)) {
  if (!schema.$ref && schema.type === 'object' && schema.properties) {
    const required = new Set(schema.required ?? []);
    const fields = Object.entries(schema.properties).map(([prop, s]) => {
      const key = /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(prop) ? prop : JSON.stringify(prop);
      return `  ${key}${required.has(prop) ? '' : '?'}: ${leafType(s)};`;
    });
    out.push(`export interface ${name} {\n${fields.join('\n')}\n}\n\n`);
  } else {
    out.push(`export type ${name} = ${leafType(schema)};\n\n`);
  }
}

// Typed path builders: the spec's route list as callable constants.
const ops = [];
for (const [path, item] of Object.entries(spec.paths)) {
  for (const method of HTTP_METHODS) {
    if (item[method]) ops.push(opModel(path, method, item[method], item.parameters));
  }
}

out.push('// Typed path constants: one builder per operation, params interpolated.\n');
out.push('export const paths = {\n');
for (const op of ops) {
  const args = op.pathParams.map((p) => `${camel(p.name)}: number | string`).join(', ');
  out.push(`  ${op.name}: (${args}) => \`${op.path.replace(/^\/api/, '').replace(/\{([^}]+)\}/g, (_, p) => `\${${camel(p)}}`)}\`,\n`);
}
out.push('} as const;\n\n');

// One wrapper per operation, on top of apiClient.
for (const op of ops) {
  out.push(`/** ${op.summary || `${op.name} (${op.path})`} */\n`);
  const args = op.pathParams.map((p) => `${camel(p.name)}: number | string`);
  const pathCall = `paths.${op.name}(${op.pathParams.map((p) => camel(p.name)).join(', ')})`;
  let querySuffix = '';
  if (op.queryParams.length > 0) {
    const fields = op.queryParams
      .map((p) => {
        const key = /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(p.name) ? p.name : JSON.stringify(p.name);
        return `${key}?: ${queryValueType(p.schema ?? {})}`;
      })
      .join('; ');
    args.push(`opts?: { query?: { ${fields} } }`);
    querySuffix = ' + toQuery(opts?.query ?? {})';
  }

  let call;
  if (op.contentType === 'application/x-www-form-urlencoded') {
    args.push(`body: ${op.bodyType}`);
    call = `apiClient.post<${op.response}>(${pathCall}${querySuffix}, new URLSearchParams(Object.entries(body).map(([k, v]) => [k, String(v)] as [string, string])))`;
  } else if (op.contentType === 'application/json') {
    args.push(`body: ${op.bodyType}`);
    call = `apiClient.request<${op.response}>(${pathCall}${querySuffix}, {
    method: '${httpMethodOf(op.name).toUpperCase()}',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })`;
  } else if (httpMethodOf(op.name) === 'get') {
    call = `apiClient.get<${op.response}>(${pathCall}${querySuffix})`;
  } else {
    call = `apiClient.request<${op.response}>(${pathCall}${querySuffix}, { method: '${httpMethodOf(op.name).toUpperCase()}' })`;
  }

  out.push(`export function ${op.name}(${args.join(', ')}): Promise<${op.response}> {
  return ${call};
}\n\n`);
}

if (ops.some((op) => op.queryParams.length > 0)) {
  out.push(`// Query params serialize the way the backend's net/http router reads them:
// repeated keys for arrays, String() for scalars, undefined dropped.
function toQuery(query: Record<string, unknown>): string {
  const sp = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined) continue;
    if (Array.isArray(value)) {
      for (const item of value) sp.append(key, String(item));
    } else {
      sp.append(key, String(value));
    }
  }
  const s = sp.toString();
  return s ? \`?\${s}\` : '';
}
`);
}

const generated = out.join('').replace(/\n{3,}/g, '\n\n');
writeFileSync(outPath, generated);
console.log(`generated.ts written: ${Object.keys(schemas).length} schemas, ${ops.length} operations`);
