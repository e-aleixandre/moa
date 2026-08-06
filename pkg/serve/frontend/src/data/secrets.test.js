import { test, expect } from "bun:test";
import { buildSecretBatch, interceptSecretCommand, MAX_SECRET_VALUE_BYTES, parseSecretCommand, secretRowsForAliases, validateSecretRows } from "./secrets.js";

test("buildSecretBatch keeps a valid batch intact", () => {
  expect(buildSecretBatch([
    { name: "db-produccion", value: "not-shown" },
    { name: "api_key", value: "also-not-shown" },
  ]).secrets).toEqual([
    { name: "db-produccion", value: "not-shown" },
    { name: "api_key", value: "also-not-shown" },
  ]);
});

test("secret validation rejects invalid, duplicate, empty, and over-limit rows without echoing values", () => {
  const duplicate = validateSecretRows([
    { name: "bad alias", value: "private-value" },
    { name: "bad alias", value: "" },
  ]);
  expect(duplicate.rows[0].name).toBeTruthy();
  expect(duplicate.rows[1].name).toBe("Alias must be unique");
  expect(duplicate.rows[1].value).toBe("Enter a secret value");
  expect(JSON.stringify(duplicate)).not.toContain("private-value");
  expect(validateSecretRows(Array.from({ length: 17 }, (_, i) => ({ name: `key${i}`, value: "x" }))).form).toContain("16");
  expect(validateSecretRows([{ name: "token", value: "é".repeat(Math.floor(MAX_SECRET_VALUE_BYTES / 2) + 1) }]).rows[0].value).toContain(String(MAX_SECRET_VALUE_BYTES));
  expect(validateSecretRows([{ name: "token", value: "x" }, { name: "TOKEN", value: "y" }]).rows[1].name).toBe("Alias must be unique");
});

test("/secret only carries valid aliases and clears the composer draft", () => {
	 expect(parseSecretCommand("/secret db-produccion api-key")).toEqual(["db-produccion", "api-key"]);
	 expect(parseSecretCommand("/secret")).toEqual([]);
	 const intercept = interceptSecretCommand("/secret db-produccion");
	 expect(intercept).toEqual({ aliases: ["db-produccion"], composerDraft: "", error: "" });
	 // Composer uses composerDraft to clear localStorage before it checks error;
	 // it deliberately has no accepted history payload for this command.
	 expect(intercept.composerDraft).toBe("");
	 expect(secretRowsForAliases(intercept.aliases)).toEqual([
		 { name: "db-produccion", value: "" },
	 ]);
});

test("/secret refuses likely inline values without retaining them", () => {
	 const invalid = interceptSecretCommand("/secret token hunter2!");
	 expect(invalid.aliases).toEqual([]);
	 expect(invalid.composerDraft).toBe("");
	 expect(invalid.error).toContain("Aliases");
	 expect(JSON.stringify(invalid)).not.toContain("hunter2!");

	 const tooMany = interceptSecretCommand("/secret one two three four");
	 expect(tooMany.aliases).toEqual([]);
	 expect(tooMany.composerDraft).toBe("");
	 expect(tooMany.error).toContain("at most");
});

test("/secret intercepts its first line even when a value follows on another line", () => {
	 const leakedValue = "do-not-send-this";
	 const newline = interceptSecretCommand(`/secret token\n${leakedValue}`);
	 expect(newline).toEqual({ aliases: ["token"], composerDraft: "", error: "" });
	 expect(JSON.stringify(newline)).not.toContain(leakedValue);

	 const crlf = interceptSecretCommand(`/secret token \r\n${leakedValue}`);
	 expect(crlf).toEqual({ aliases: ["token"], composerDraft: "", error: "" });
	 expect(JSON.stringify(crlf)).not.toContain(leakedValue);
});
