import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(
  new URL(
    "../charts/private-llm-operator/files/ui-extensions/ord/renderer.js",
    import.meta.url,
  ),
  "utf8",
);
const { configUrlFor, loadProviderDocuments, parseProviderData, render } =
  await import(
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
            url: "./private-llm.json",
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
  "https://llm.example/ord/private-llm.json",
]);
assert.equal(result.documents.length, 1);

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

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName;
    this.children = [];
    this.attributes = {};
    this.textContent = "";
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
              { type: "openapi-v3", url: "./second-openapi.json" },
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
  ["Product", "Provider", "System version"],
);
assert.equal(
  flattened.find(({ tagName }) => tagName === "a").href,
  "https://llm.example/ord/second-openapi.json",
);
assert.equal(textOf(app).includes("Private API"), false);
assert.equal(textOf(app).includes("Public APIs2"), true);

function flatten(node) {
  return [node, ...node.children.flatMap(flatten)];
}

function textOf(node) {
  return node.textContent + node.children.map(textOf).join("");
}
