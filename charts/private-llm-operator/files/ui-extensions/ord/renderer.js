const PROTOCOL = "platform-mesh.provider-details.v1";
const NAVIGATE = "platform-mesh.provider-details.navigate.v1";
const RESIZE = "platform-mesh.provider-details.resize.v1";
const MAX_DOCUMENTS = 10;
const MAX_PROVIDERS = 32;
const PROVIDER_BATCH = 4;
const MAX_BYTES = 2 * 1024 * 1024;

export function parseProviderData(data) {
  if (!data) return {};
  if (typeof data === "object") return data;
  if (typeof data !== "string") return {};
  try {
    const parsed = JSON.parse(data);
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

export function configUrlFor(provider) {
  const data = parseProviderData(provider?.providerMetadata?.spec?.data);
  const url = data?.ord?.configUrl;
  if (typeof url !== "string") return undefined;
  try {
    const parsed = new URL(url);
    return ["http:", "https:"].includes(parsed.protocol)
      ? parsed.href
      : undefined;
  } catch {
    return undefined;
  }
}

export async function loadProviderDocuments(provider, fetchJSON = fetchJson) {
  const configUrl = configUrlFor(provider);
  if (!configUrl) return { configUrl, documents: [] };
  const configuration = await fetchJSON(configUrl, 256 * 1024);
  const declarations = configuration?.openResourceDiscoveryV1?.documents;
  if (!Array.isArray(declarations))
    throw new Error("Invalid ORD configuration");
  const openDocuments = declarations
    .filter((document) =>
      (document.accessStrategies ?? []).some(
        (strategy) => strategy.type === "open",
      ),
    )
    .slice(0, MAX_DOCUMENTS);
  const configurationBaseUrl = safeHttpUrl(configuration.baseUrl, configUrl);
  const documents = await Promise.all(
    openDocuments.map(async (document) => {
      const sourceUrl = resolveConfigurationReference(
        document.url,
        configUrl,
        configurationBaseUrl,
      );
      if (!sourceUrl) throw new Error("Invalid ORD document URL");
      const payload = await fetchJSON(sourceUrl, MAX_BYTES);
      if (payload?.openResourceDiscovery !== "1.16") {
        throw new Error("Unsupported ORD version");
      }
      return { sourceUrl, document: payload };
    }),
  );
  return { configUrl, documents };
}

// ORD compatibility versions describe a major/minor contract, as in Chat UI.
function matchesVersion(minVersion, maxVersion) {
  if (!minVersion) return true;
  const pair = (value) => /^(\d+)\.(\d+)(?:\.\d+)?$/.exec(value ?? "");
  const required = pair(minVersion);
  const offered = pair(maxVersion);
  return Boolean(
    required &&
      offered &&
      Number(required[1]) === Number(offered[1]) &&
      Number(required[2]) <= Number(offered[2]),
  );
}

export function matchConsumers(currentDocuments, providerDocuments) {
  const apis = currentDocuments.flatMap(({ document }) =>
    (document.apiResources ?? []).filter(
      (api) => api.visibility === "public" && !api.abstract,
    ),
  );
  return providerDocuments.flatMap(({ provider, documents }) => {
    const requirements = documents.flatMap(({ document }) =>
      (document.integrationDependencies ?? []).flatMap((dependency) =>
        (dependency.aspects ?? []).flatMap(
          (aspect) => aspect.apiResources ?? [],
        ),
      ),
    );
    const matched = apis.filter((api) =>
      requirements.some((requirement) =>
        [
          { ordId: api.ordId, maxVersion: api.version },
          ...(api.compatibleWith ?? []),
        ].some(
          (offered) =>
            offered.ordId === requirement.ordId &&
            matchesVersion(requirement.minVersion, offered.maxVersion),
        ),
      ),
    );
    return matched.length ? [{ provider, apis: matched }] : [];
  });
}

export async function loadConsumers(
  context,
  currentDocuments,
  fetchJSON = fetchJson,
) {
  const providers = [
    ...new Map(
      (context.providers ?? [])
        .filter(
          (provider) =>
            provider.name !== context.currentProvider.name &&
            configUrlFor(provider),
        )
        .map((provider) => [provider.name, provider]),
    ).values(),
  ].slice(0, MAX_PROVIDERS);
  const consumers = [];
  for (let start = 0; start < providers.length; start += PROVIDER_BATCH) {
    const results = await Promise.allSettled(
      providers.slice(start, start + PROVIDER_BATCH).map(async (provider) =>
        matchConsumers(currentDocuments, [
          {
            provider,
            ...(await loadProviderDocuments(provider, fetchJSON)),
          },
        ]),
      ),
    );
    consumers.push(
      ...results
        .filter((result) => result.status === "fulfilled")
        .flatMap((result) => result.value),
    );
  }
  return consumers;
}

async function fetchJson(url, maxBytes) {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), 5000);
  try {
    const response = await fetch(url, {
      credentials: "omit",
      redirect: "error",
      referrerPolicy: "no-referrer",
      signal: controller.signal,
    });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const contentType = response.headers.get("content-type") ?? "";
    if (
      !contentType.includes("application/json") &&
      !contentType.includes("+json")
    ) {
      throw new Error("Response is not JSON");
    }
    const text = await response.text();
    if (new TextEncoder().encode(text).byteLength > maxBytes) {
      throw new Error("Response is too large");
    }
    return JSON.parse(text);
  } finally {
    window.clearTimeout(timeout);
  }
}

function element(tag, className, value) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (value !== undefined) node.textContent = value ?? "";
  return node;
}

function first(items) {
  return items.find(Boolean);
}

function addIdentity(list, label, value) {
  if (!value) return;
  const item = element("div");
  item.append(element("dt", "", label), element("dd", "", value));
  list.append(item);
}

function externalLink(label, url) {
  const link = element("a", "", `${label} ↗`);
  link.href = url;
  link.target = "_blank";
  link.rel = "noopener noreferrer";
  link.title = `${label} — opens in a new tab`;
  link.setAttribute("aria-label", `${label} — opens in a new tab`);
  return link;
}

function addIdentityLink(list, label, linkLabel, url) {
  if (!url) return;
  const item = element("div");
  const value = element("dd");
  value.append(externalLink(linkLabel, url));
  item.append(element("dt", "", label), value);
  list.append(item);
}

function definitionLabel(type) {
  if (type === "openapi-v3") return "OpenAPI 3 specification";
  if (type === "asyncapi-v2") return "AsyncAPI specification";
  return "API specification";
}

export async function render(context) {
  const app = document.querySelector("#app");
  if (!app) return;
  if (context?.protocolVersion !== PROTOCOL) {
    app.replaceChildren(
      element("p", "error", "Additional service information is unavailable."),
    );
    return;
  }

  return loadProviderDocuments(context.currentProvider)
    .then(async ({ configUrl, documents }) => {
      const systemEntry = documents.find(
        ({ document }) => document.perspective === "system-version",
      );
      const system = systemEntry?.document;
      if (!system) throw new Error("System document is missing");

      const product = first(system.products ?? []);
      const packageInfo = first(system.packages ?? []);
      const vendorId = packageInfo?.vendor ?? product?.vendor;
      const vendor = (system.vendors ?? []).find(
        (entry) => entry.ordId === vendorId,
      );
      app.replaceChildren();
      const identity = element("dl", "identity");
      addIdentity(identity, "Product", product?.title ?? packageInfo?.title);
      addIdentity(identity, "Provider", vendor?.title);
      addIdentity(
        identity,
        "System version",
        system.describedSystemVersion?.version,
      );
      addIdentityLink(identity, "ORD", "Document", systemEntry.sourceUrl);
      app.append(identity);

      const apis = (system.apiResources ?? []).filter(
        (api) => api.visibility === "public",
      );
      if (apis.length) {
        const heading = element("div", "section-heading");
        heading.append(
          element("h2", "", "Public APIs"),
          element("span", "count", String(apis.length)),
        );
        app.append(heading);
      }
      const apiList = element("section", "api-list");
      apiList.setAttribute("aria-label", "Public API specifications");
      for (const api of apis) {
        const card = element("article", "api");
        const content = element("div", "api__content");
        content.append(element("h3", "", api.title || api.ordId));
        const description = api.shortDescription || api.description;
        if (description) {
          content.append(element("p", "description", description));
        }
        const badges = element("div", "badges");
        for (const value of [
          api.apiProtocol?.toUpperCase(),
          api.version,
          api.releaseStatus,
        ].filter(Boolean)) {
          badges.append(element("span", "badge", value));
        }
        const actions = element("div", "api__actions");
        actions.append(badges);
        for (const definition of api.resourceDefinitions ?? []) {
          const definitionUrl = resolveMetadataReference(
            definition.url,
            system,
            systemEntry.sourceUrl,
            configUrl,
          );
          if (!definitionUrl) continue;
          actions.append(
            externalLink(definitionLabel(definition.type), definitionUrl),
          );
        }
        card.append(content, actions);
        apiList.append(card);
      }
      if (apis.length) app.append(apiList);

      const consumers = await loadConsumers(context, documents);
      if (consumers.length) {
        const heading = element("div", "section-heading consumers-heading");
        heading.append(
          element("h2", "", "Used by"),
          element("span", "count", String(consumers.length)),
        );
        const list = element("section", "api-list");
        list.setAttribute("aria-label", "Services using these APIs");
        for (const { provider, apis: usedApis } of consumers) {
          const card = element("button", "api consumer");
          card.type = "button";
          card.addEventListener("click", () =>
            sendMessage(NAVIGATE, { providerName: provider.name }),
          );
          const content = element("span", "api__content");
          content.append(
            element(
              "strong",
              "",
              provider.providerMetadata?.spec?.displayName || provider.name,
            ),
            element(
              "span",
              "description",
              usedApis.map((api) => api.title || api.ordId).join(" · "),
            ),
          );
          card.append(content, element("span", "", "›"));
          list.append(card);
        }
        app.append(heading, list);
      }
    })
    .catch(() => {
      app.replaceChildren(
        element("p", "error", "Additional service information is unavailable."),
      );
    });
}

function safeHttpUrl(value, baseUrl) {
  if (typeof value !== "string") return undefined;
  try {
    const url = new URL(value, baseUrl);
    return ["http:", "https:"].includes(url.protocol) ? url.href : undefined;
  } catch {
    return undefined;
  }
}

function resolveConfigurationReference(value, configUrl, explicitBaseUrl) {
  if (safeHttpUrl(value)) return safeHttpUrl(value);
  if (typeof value !== "string") return undefined;
  if (explicitBaseUrl) {
    return value.startsWith("/")
      ? appendToBaseUrl(explicitBaseUrl, value)
      : safeHttpUrl(value, `${explicitBaseUrl.replace(/\/$/, "")}/`);
  }
  if (value.startsWith("/")) {
    const implicitBaseUrl = implicitConfigurationBaseUrl(configUrl);
    return implicitBaseUrl
      ? appendToBaseUrl(implicitBaseUrl, value)
      : safeHttpUrl(value, configUrl);
  }
  return safeHttpUrl(value, configUrl);
}

export function resolveMetadataReference(
  value,
  document,
  documentUrl,
  configUrl,
) {
  const absolute = safeHttpUrl(value);
  if (absolute) return absolute;
  if (typeof value !== "string" || !documentUrl) return undefined;
  if (!value.startsWith("/")) return safeHttpUrl(value, documentUrl);

  const documentBaseUrl = safeHttpUrl(document?.baseUrl, documentUrl);
  if (documentBaseUrl) return appendToBaseUrl(documentBaseUrl, value);

  const fetchContextUrl = safeHttpUrl(documentUrl);
  if (fetchContextUrl) return safeHttpUrl(value, fetchContextUrl);

  const legacyBaseUrl = safeHttpUrl(
    document?.describedSystemInstance?.baseUrl,
    documentUrl,
  );
  if (legacyBaseUrl) return appendToBaseUrl(legacyBaseUrl, value);

  const configBaseUrl = implicitConfigurationBaseUrl(configUrl);
  return configBaseUrl ? appendToBaseUrl(configBaseUrl, value) : undefined;
}

function appendToBaseUrl(baseUrl, reference) {
  const base = safeHttpUrl(baseUrl);
  if (!base) return undefined;
  try {
    const url = new URL(base);
    const parsedReference = new URL(reference, "https://relative.invalid");
    url.pathname = `${url.pathname.replace(
      /\/$/,
      "",
    )}/${parsedReference.pathname.replace(/^\/+/u, "")}`;
    url.search = parsedReference.search;
    url.hash = parsedReference.hash;
    return url.href;
  } catch {
    return undefined;
  }
}

function implicitConfigurationBaseUrl(configUrl) {
  const url = safeHttpUrl(configUrl);
  if (!url) return undefined;
  const parsed = new URL(url);
  const suffix = "/.well-known/open-resource-discovery";
  if (parsed.pathname.endsWith(suffix)) {
    parsed.pathname = parsed.pathname.slice(0, -suffix.length) || "/";
    parsed.search = "";
    parsed.hash = "";
    return parsed.href.replace(/\/$/, "");
  }
  return undefined;
}

let targetOrigin = "*";

function sendMessage(id, data) {
  window.parent.postMessage(
    { msg: "custom", data: { id, ...data } },
    targetOrigin,
  );
}

function initLuigiClient() {
  window.addEventListener("message", (event) => {
    if (event.source !== window.parent || event.data?.msg !== "luigi.init")
      return;
    targetOrigin = event.origin;
    const rawContext = event.data.context;
    window.parent.postMessage({ msg: "luigi.init.ok" }, targetOrigin);
    try {
      const context =
        typeof rawContext === "string" ? JSON.parse(rawContext) : rawContext;
      render(context);
    } catch {
      render(undefined);
    }
  });
  window.parent.postMessage(
    { msg: "luigi.get-context", clientVersion: "2.22.1" },
    "*",
  );
  new ResizeObserver(() =>
    sendMessage(RESIZE, { height: document.documentElement.scrollHeight }),
  ).observe(document.documentElement);
}

if (typeof window !== "undefined" && window.parent !== window) {
  initLuigiClient();
}
