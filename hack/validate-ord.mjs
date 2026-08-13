import { readdir, readFile } from "node:fs/promises";
import { basename, join, resolve } from "node:path";
import Ajv from "ajv";
import addFormats from "ajv-formats";
import SwaggerParser from "@apidevtools/swagger-parser";
import {
  ordConfigurationSchema,
  ordDocumentSchema,
} from "@open-resource-discovery/specification";

const root = resolve(process.argv[2] ?? ".");
const releaseManifest = JSON.parse(
  await readFile(resolve(".release-please-manifest.json"), "utf8"),
);
const releaseVersion = releaseManifest["."];
const entries = (await readdir(root)).filter((name) => name.endsWith(".json"));
const ajv = new Ajv({ allErrors: true, strict: false });
addFormats(ajv);
const validateConfiguration = ajv.compile(ordConfigurationSchema);
const validateDocument = ajv.compile(ordDocumentSchema);
const names = new Set(entries);

for (const name of entries) {
  const file = join(root, name);
  const value = JSON.parse(await readFile(file, "utf8"));
  if (name.endsWith(".oas3.json")) {
    await SwaggerParser.validate(file);
    console.log(`validated OpenAPI: ${name}`);
    continue;
  }

  const validate = name === "configuration.json" ? validateConfiguration : validateDocument;
  if (!validate(value)) {
    throw new Error(`${name} is invalid: ${ajv.errorsText(validate.errors)}`);
  }
  if (name !== "configuration.json" && value.openResourceDiscovery !== "1.16") {
    throw new Error(`${name} uses unsupported ORD version ${value.openResourceDiscovery}`);
  }
  if (
    value.describedSystemVersion?.version &&
    value.describedSystemVersion.version !== releaseVersion
  ) {
    throw new Error(
      `${name} describes system version ${value.describedSystemVersion.version}, expected ${releaseVersion}`,
    );
  }

  for (const resource of value.apiResources ?? []) {
    for (const definition of resource.resourceDefinitions ?? []) {
      const definitionName = basename(new URL(definition.url, "https://provider.invalid").pathname);
      if (!names.has(definitionName)) {
        throw new Error(`${name} references missing definition ${definition.url}`);
      }
    }
  }
  console.log(`validated ORD: ${name}`);
}
