import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import Ajv2020 from "ajv/dist/2020.js";
import yaml from "js-yaml";

const contract = yaml.load(readFileSync(new URL("../openapi/v1.yaml", import.meta.url), "utf8"));
const ajv = new Ajv2020({ strict: false, validateFormats: false });
const operationID = "01J00000000000000000000000";
const validators = Object.fromEntries(["TopicCreate", "TopicReplace", "BatchTopicCreate", "LinkedInCreate", "LinkedInReplace", "BatchLinkedInCreate"].map((name) => [name, ajv.compile({ components: contract.components, $ref: `#/components/schemas/${name}` })]));

for (const [schema, validate] of Object.entries(validators)) {
  const isBatch = schema.startsWith("Batch");
  const isTopic = schema.includes("Topic");
  const request = {
    type: isTopic ? "topic" : "linkedin", working_title: "One idea", status: "draft",
    ...(isBatch ? {} : { operation_id: operationID }),
    ...(schema.endsWith("Replace") ? { revision: 1 } : {}),
    content: isTopic ? { source: "Source notes", source_url: "https://docs.google.com/document/d/source" } : { body: "Related post" },
  };
  test(`${schema} accepts standalone resources and validates topic relationships`, () => {
    assert.equal(validate(request), true, JSON.stringify(validate.errors));
    assert.equal(validate({ ...request, topic_id: "" }), true);
    assert.equal(validate({ ...request, topic_id: operationID }), !isTopic);
    assert.equal(validate({ ...request, topic_id: operationID.toLowerCase() }), false);
    for (const topic_id of ["not-an-id", "81J00000000000000000000000", "01I00000000000000000000000"]) {
      assert.equal(validate({ ...request, topic_id }), false, `accepted invalid topic ID: ${topic_id}`);
    }
    assert.equal(validate({ ...request, scheduled_at: "2026-09-19T09:00:00Z" }), !isTopic);
  });
}
