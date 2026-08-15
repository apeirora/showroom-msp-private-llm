import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(
  new URL(
    "../charts/private-llm-operator/files/ui-extensions/ord/renderer.js",
    import.meta.url,
  ),
  "utf8",
);
const { configUrlFor, loadProviderDocuments, parseProviderData } = await import(
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
