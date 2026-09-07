#!/usr/bin/env python3
"""Check the token route contract used by the portal detail actions."""

import json
import unittest
from pathlib import Path


CONTENT = Path(__file__).resolve().parents[1] / "charts/private-llm-operator/files/pm-content.json"


class TokenRequestUITest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.nodes = json.loads(CONTENT.read_text())["luigiConfigFragment"]["data"]["nodes"]
        cls.token = next(n for n in cls.nodes if n.get("label") == "Token Requests")

    def test_components_load_as_modules_without_prior_navigation(self):
        components = [n for n in self.nodes if "webcomponent" in n]
        self.assertTrue(components)
        for node in components:
            with self.subTest(url=node["url"]):
                # Luigi uses a classic script unless the module type is explicit.
                self.assertEqual(node["webcomponent"].get("type"), "module")

    def test_token_row_reaches_detail_actions_for_selected_resource(self):
        definition = self.token["context"]["resourceDefinition"]
        self.assertTrue(definition["ui"].get("detailView"), "The portal ignores row clicks without detailView")
        self.assertEqual(definition["scope"], "Namespaced")
        self.assertTrue(self.token["navigationContext"], "Delete must return to the token list")
        child, = self.token["children"]
        self.assertEqual(child["context"]["resourceId"], child["pathSegment"])
        entity_type = self.token["entityType"] + "." + child["defineEntity"]["id"]
        detail, = [n for n in self.nodes if n.get("entityType") == entity_type]
        self.assertTrue(detail["url"].endswith("#generic-detail-view"))
        self.assertTrue(detail["webcomponent"]["selfRegistered"])

    def test_edit_form_reads_spec_fields_and_excludes_status_and_secret_data(self):
        list_definition = self.token["context"]["resourceDefinition"]
        create_fields = {f["property"] for f in list_definition["ui"]["createView"]["fields"]}
        self.assertEqual(create_fields, {"metadata.name", "spec.instanceName", "spec.description"})
        child, = self.token["children"]
        entity_type = self.token["entityType"] + "." + child["defineEntity"]["id"]
        detail, = [n for n in self.nodes if n.get("entityType") == entity_type]
        detail_definition = detail["context"]["resourceDefinition"]
        for key in ("apiGroup", "version", "entity", "entityCollection", "scope", "namespace"):
            self.assertEqual(detail_definition[key], list_definition[key])
        ui = detail_definition["ui"]
        edit_fields = {f["property"] for f in ui["createView"]["fields"]}
        self.assertEqual(edit_fields, {"metadata.name", "spec.description"})
        # Portal 0.51.0 reads createView fields for both the detail page and edit form.
        display_fields = {f["property"] for f in ui["detailView"]["fields"]}
        self.assertTrue(display_fields <= edit_fields)

    def test_generated_secrets_have_no_mutation_route(self):
        secret = next(n for n in self.nodes if n.get("label") == "Secrets")
        ui = secret["context"]["resourceDefinition"]["ui"]
        self.assertNotIn("createView", ui)
        self.assertNotIn("detailView", ui)
        self.assertFalse(secret.get("children"))


if __name__ == "__main__":
    unittest.main()
