import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(
  new URL(
    "../charts/private-llm-operator/files/ui-extensions/ord/renderer.js",
    import.meta.url,
  ),
  "utf8",
);
const {
  configUrlFor,
  loadProviderDocuments,
  loadConsumers,
  matchConsumers,
  parseProviderData,
  render,
  resolveMetadataReference,
} = await import(
  `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`
);

const provider = {
  name: "private-llm",
  providerMetadata: {
    spec: {
      displayName: "Private LLM",
      data: {
        ord: { configUrl: "https://llm.example/ord/configuration.json" },
      },
    },
  },
};

assert.deepEqual(
  parseProviderData('{"ord":{"configUrl":"https://example.test"}}'),
  {
    ord: { configUrl: "https://example.test" },
  },
);
assert.equal(
  configUrlFor(provider),
  "https://llm.example/ord/configuration.json",
);

const requested = [];
const fetchJSON = async (url) => {
  requested.push(url);
  if (url.endsWith("configuration.json")) {
    return {
      openResourceDiscoveryV1: {
        documents: [
          {
            url: "/ord/documents/private-llm.json",
            accessStrategies: [{ type: "open" }],
          },
          {
            url: "./private.json",
            accessStrategies: [{ type: "oauth2" }],
          },
        ],
      },
    };
  }
  return { openResourceDiscovery: "1.16", apiResources: [] };
};

const result = await loadProviderDocuments(provider, fetchJSON);
assert.deepEqual(requested, [
  "https://llm.example/ord/configuration.json",
  "https://llm.example/ord/documents/private-llm.json",
]);
assert.equal(result.documents.length, 1);

const prefixedProvider = {
  providerMetadata: {
    spec: {
      data: {
        ord: {
          configUrl:
            "https://provider.test/api/v1/.well-known/open-resource-discovery",
        },
      },
    },
  },
};
const prefixedRequests = [];
await loadProviderDocuments(prefixedProvider, async (url) => {
  prefixedRequests.push(url);
  return url.endsWith("open-resource-discovery")
    ? {
        openResourceDiscoveryV1: {
          documents: [
            {
              url: "/documents/system.json",
              accessStrategies: [{ type: "open" }],
            },
          ],
        },
      }
    : { openResourceDiscovery: "1.16" };
});
assert.deepEqual(prefixedRequests, [
  "https://provider.test/api/v1/.well-known/open-resource-discovery",
  "https://provider.test/api/v1/documents/system.json",
]);

await assert.rejects(
  loadProviderDocuments(provider, async (url) =>
    url.endsWith("configuration.json")
      ? {
          openResourceDiscoveryV1: {
            documents: [
              { url: "./document.json", accessStrategies: [{ type: "open" }] },
            ],
          },
        }
      : { openResourceDiscovery: "1.17" },
  ),
  /Unsupported ORD version/,
);

await assert.rejects(
  loadProviderDocuments(provider, async () => ({
    openResourceDiscoveryV1: {
      documents: [
        {
          url: "data:application/json,e30=",
          accessStrategies: [{ type: "open" }],
        },
      ],
    },
  })),
  /Invalid ORD document URL/,
);

assert.equal(
  resolveMetadataReference(
    "/schemas/chat.oas3.json?version=2#operations",
    { baseUrl: "https://provider.example/api/v1" },
    "https://provider.example/ord/documents/private-llm.json",
    "https://provider.example/.well-known/open-resource-discovery",
  ),
  "https://provider.example/api/v1/schemas/chat.oas3.json?version=2#operations",
);
assert.equal(
  resolveMetadataReference(
    "../schemas/chat.oas3.json",
    { baseUrl: "https://provider.example/api/v1" },
    "https://provider.example/ord/documents/private-llm.json",
    "https://provider.example/.well-known/open-resource-discovery",
  ),
  "https://provider.example/ord/schemas/chat.oas3.json",
);

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName;
    this.children = [];
    this.attributes = {};
    this.textContent = "";
    this.listeners = {};
  }

  addEventListener(name, listener) {
    this.listeners[name] = listener;
  }

  append(...children) {
    this.children.push(...children);
  }

  replaceChildren(...children) {
    this.children = children;
  }

  setAttribute(name, value) {
    this.attributes[name] = value;
  }
}

const app = new FakeElement("main");
globalThis.document = {
  querySelector: (selector) => (selector === "#app" ? app : undefined),
  createElement: (tagName) => new FakeElement(tagName),
};
globalThis.window = { setTimeout, clearTimeout };
globalThis.fetch = async (url) => {
  const payload = url.endsWith("configuration.json")
    ? {
        openResourceDiscoveryV1: {
          documents: [
            {
              url: "./document.json",
              accessStrategies: [{ type: "open" }],
            },
          ],
        },
      }
    : {
        openResourceDiscovery: "1.16",
        perspective: "system-version",
        baseUrl: "https://metadata.example/api/v1",
        describedSystemVersion: { version: "2.15.5" },
        products: [{ title: "Private LLM", vendor: "vendor:one" }],
        vendors: [{ ordId: "vendor:one", title: "ApeiroRA" }],
        apiResources: [
          {
            title: "Second public API",
            visibility: "public",
            apiProtocol: "rest",
            version: "2.0.0",
            resourceDefinitions: [
              { type: "openapi-v3", url: "/schemas/second-openapi.json" },
            ],
          },
          { title: "Private API", visibility: "internal" },
          { title: "First public API", visibility: "public" },
        ],
      };
  return new Response(JSON.stringify(payload), {
    headers: { "content-type": "application/json" },
  });
};

await render({
  protocolVersion: "platform-mesh.provider-details.v1",
  currentProvider: provider,
});

const flattened = flatten(app);
assert.deepEqual(
  flattened.filter(({ tagName }) => tagName === "h3").map(textOf),
  ["Second public API", "First public API"],
);
assert.deepEqual(
  flattened.filter(({ tagName }) => tagName === "dt").map(textOf),
  ["Product", "Provider", "System version", "ORD"],
);
assert.deepEqual(
  flattened.filter(({ tagName }) => tagName === "a").map(({ href }) => href),
  [
    "https://llm.example/ord/document.json",
    "https://metadata.example/api/v1/schemas/second-openapi.json",
  ],
);
assert.equal(textOf(app).includes("ORDDocument ↗"), true);
assert.equal(textOf(app).includes("Private API"), false);
assert.equal(textOf(app).includes("Public APIs2"), true);

function flatten(node) {
  return [node, ...node.children.flatMap(flatten)];
}

function textOf(node) {
  return node.textContent + node.children.map(textOf).join("");
}

// The consumer declares a requirement on a shared contract, not on Private LLM.
const contract = "apeirora.eu:apiResource:openai-chat-completions:v1";
const llmApi = {
  ordId: "apeirora.eu:apiResource:private-llm-chat-completions:v1",
  title: "Chat Completions API",
  visibility: "public",
  version: "1.0.0",
  compatibleWith: [{ ordId: contract, maxVersion: "1.0" }],
};
const currentDocuments = [{ document: { apiResources: [llmApi] } }];
const chat = {
  name: "ui.privatellms.msp",
  providerMetadata: {
    spec: {
      displayName: "Chat UI",
      data: {
        ord: { configUrl: "https://chat.example/configuration.json" },
      },
    },
  },
};
const chatDocument = {
  openResourceDiscovery: "1.16",
  integrationDependencies: [
    {
      ordId: "llm-backend",
      aspects: [
        {
          apiResources: [{ ordId: contract, minVersion: "1.0.0" }],
        },
      ],
    },
  ],
};
const catalog = (document, candidate = chat) => [
  {
    provider: candidate,
    documents: [{ document }],
  },
];
assert.equal(
  matchConsumers(currentDocuments, catalog(chatDocument))[0].provider.name,
  chat.name,
);
for (const version of ["1.1.0", "2.0.0", "invalid"]) {
  const incompatible = structuredClone(chatDocument);
  incompatible.integrationDependencies[0].aspects[0].apiResources[0].minVersion =
    version;
  assert.deepEqual(matchConsumers(currentDocuments, catalog(incompatible)), []);
}
const direct = structuredClone(chatDocument);
direct.integrationDependencies[0].aspects[0].apiResources[0].ordId =
  llmApi.ordId;
assert.equal(matchConsumers(currentDocuments, catalog(direct)).length, 1);
for (const change of [{ visibility: "internal" }, { abstract: true }]) {
  assert.deepEqual(
    matchConsumers(
      [{ document: { apiResources: [{ ...llmApi, ...change }] } }],
      catalog(chatDocument),
    ),
    [],
  );
}

const broken = structuredClone(chat);
broken.name = "unavailable";
broken.providerMetadata.spec.data.ord.configUrl =
  "https://broken.example/configuration.json";
const malformed = structuredClone(chat);
malformed.name = "malformed";
malformed.providerMetadata.spec.data.ord.configUrl =
  "https://malformed.example/configuration.json";
const consumerRequests = [];
let active = 0;
let peak = 0;
const fetchConsumerJSON = async (url) => {
  consumerRequests.push(url);
  active++;
  peak = Math.max(peak, active);
  await Promise.resolve();
  active--;
  if (url.includes("broken.example")) throw new Error("catalog unavailable");
  if (url.endsWith("configuration.json"))
    return {
      openResourceDiscoveryV1: {
        documents: [
          { url: "./consumer.json", accessStrategies: [{ type: "open" }] },
        ],
      },
    };
  if (url.includes("malformed.example"))
    return { openResourceDiscovery: "1.16", integrationDependencies: {} };
  return chatDocument;
};
const context = {
  protocolVersion: "platform-mesh.provider-details.v1",
  currentProvider: provider,
  providers: [provider, broken, malformed, chat, chat],
};
const consumers = await loadConsumers(
  context,
  currentDocuments,
  fetchConsumerJSON,
);
assert.equal(consumers.length, 1);
assert.equal(consumers[0].provider.name, chat.name);
assert.equal(
  consumerRequests.filter((url) => url === configUrlFor(chat)).length,
  1,
);
assert.equal(consumerRequests.includes(configUrlFor(provider)), false);
const many = Array.from({ length: 40 }, (_, i) => ({
  ...chat,
  name: `consumer-${i}`,
}));
assert.equal(
  (
    await loadConsumers(
      { ...context, providers: many },
      currentDocuments,
      fetchConsumerJSON,
    )
  ).length,
  32,
);
assert.ok(peak <= 4, `catalog concurrency was ${peak}`);

const originalFetch = globalThis.fetch;
globalThis.fetch = async (url, options) => {
  if (url.startsWith("https://llm.example/")) {
    if (url.endsWith("configuration.json")) return originalFetch(url, options);
    return new Response(
      JSON.stringify({
        openResourceDiscovery: "1.16",
        perspective: "system-version",
        products: [{ title: "Private LLM" }],
        apiResources: [llmApi],
      }),
      { headers: { "content-type": "application/json" } },
    );
  }
  return new Response(JSON.stringify(await fetchConsumerJSON(url)), {
    headers: { "content-type": "application/json" },
  });
};
const messages = [];
globalThis.window.parent = { postMessage: (...args) => messages.push(args) };
await render(context);
assert.ok(textOf(app).includes("Public APIs1"));
assert.ok(textOf(app).includes("Used by1"));
assert.ok(textOf(app).includes("Chat UI"));
const consumerButton = flatten(app).find((node) => node.tagName === "button");
consumerButton.listeners.click();
assert.deepEqual(messages[0][0], {
  msg: "custom",
  data: {
    id: "platform-mesh.provider-details.navigate.v1",
    providerName: chat.name,
  },
});
await render({ ...context, providers: [broken] });
assert.ok(textOf(app).includes("Public APIs1"));
assert.equal(textOf(app).includes("Used by"), false);
console.log(
  "ORD renderer: identity, APIs, reverse consumers, versions, isolation, bounds and navigation passed",
);
