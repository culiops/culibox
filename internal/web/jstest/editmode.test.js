"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { resolveEditorMode } = require("../static/editmode.js");

test("infra-as-code by extension", () => {
  assert.strictEqual(resolveEditorMode("main.tf"), "hcl");
  assert.strictEqual(resolveEditorMode("vars.tfvars"), "hcl");
  assert.strictEqual(resolveEditorMode("config.toml"), "toml");
  assert.strictEqual(resolveEditorMode("app.yaml"), "yaml");
  assert.strictEqual(resolveEditorMode("k8s.yml"), "yaml");
});

test("ops config files (ext meta.js misses)", () => {
  assert.strictEqual(resolveEditorMode("redis.conf"), "properties");
  assert.strictEqual(resolveEditorMode(".env"), "properties");
  assert.strictEqual(resolveEditorMode("app.ini"), "properties");
  assert.strictEqual(resolveEditorMode("site.nginx"), "nginx");
});

test("filename-based (no extension)", () => {
  assert.strictEqual(resolveEditorMode("/srv/app/Dockerfile"), "dockerfile");
  assert.strictEqual(resolveEditorMode("Jenkinsfile"), "groovy");
  assert.strictEqual(resolveEditorMode("Makefile"), "makefile");
  assert.strictEqual(resolveEditorMode("GNUmakefile"), "makefile");
  assert.strictEqual(resolveEditorMode("build.mk"), "makefile");
});

test("scripting + code", () => {
  assert.strictEqual(resolveEditorMode("deploy.sh"), "text/x-sh");
  assert.strictEqual(resolveEditorMode("task.py"), "python");
  assert.strictEqual(resolveEditorMode("main.go"), "go");
  assert.strictEqual(resolveEditorMode("lib.rs"), "rust");
  assert.strictEqual(resolveEditorMode("app.rb"), "ruby");
  assert.strictEqual(resolveEditorMode("query.sql"), "text/x-sql");
  assert.strictEqual(resolveEditorMode("script.ps1"), "powershell");
  assert.strictEqual(resolveEditorMode("Config.groovy"), "groovy");
  assert.strictEqual(resolveEditorMode("data.json"), "application/json");
});

test("unknown → null (caller uses plain text)", () => {
  assert.strictEqual(resolveEditorMode("mystery.xyzzy"), null);
  assert.strictEqual(resolveEditorMode("noext"), null);
});

test("case-insensitive extension + path stripping", () => {
  assert.strictEqual(resolveEditorMode("/a/b/MAIN.TF"), "hcl");
  assert.strictEqual(resolveEditorMode("A.YAML"), "yaml");
});
