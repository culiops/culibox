"use strict";

// Pure filename/extension -> CodeMirror mode/MIME resolver for the editor.
// Returns null for unknown types (the caller falls back to plain text). Kept
// DOM-free and CodeMirror-free so `node --test` can cover it. Covers the
// ops/devops/cloud/SRE/platform set, including files upstream meta.js does not
// map (Dockerfile/Jenkinsfile/Makefile by name; .conf/.env/.tf by extension).

// By whole filename (no extension, or name is the signal).
const BY_NAME = {
  "dockerfile": "dockerfile",
  "containerfile": "dockerfile",
  "jenkinsfile": "groovy",
  "makefile": "makefile",
  "gnumakefile": "makefile",
  "vagrantfile": "ruby",
  "gemfile": "ruby",
  "rakefile": "ruby",
  "berksfile": "ruby",
  "brewfile": "ruby",
  ".env": "properties",
  ".bashrc": "text/x-sh",
  ".bash_profile": "text/x-sh",
  ".zshrc": "text/x-sh",
  ".profile": "text/x-sh",
  ".gitconfig": "properties",
};

// By lowercased extension.
const BY_EXT = {
  // IaC / config / data
  tf: "hcl", tfvars: "hcl", hcl: "hcl",
  yaml: "yaml", yml: "yaml",
  json: "application/json",
  toml: "toml",
  ini: "properties", conf: "properties", cfg: "properties",
  properties: "properties", env: "properties",
  nginx: "nginx",
  xml: "xml", xsd: "xml", plist: "xml", svg: "xml",
  md: "markdown", markdown: "markdown",
  dockerfile: "dockerfile",
  mk: "makefile", make: "makefile",
  cmake: "cmake",
  // shells / scripting
  sh: "text/x-sh", bash: "text/x-sh", zsh: "text/x-sh", ksh: "text/x-sh",
  py: "python", pyw: "python",
  rb: "ruby",
  pl: "perl", pm: "perl",
  lua: "lua",
  ps1: "powershell", psm1: "powershell", psd1: "powershell",
  groovy: "groovy", gradle: "groovy",
  // compiled / platform code
  go: "go",
  rs: "rust",
  java: "text/x-java", kt: "text/x-kotlin", scala: "text/x-scala",
  c: "text/x-csrc", h: "text/x-csrc",
  cpp: "text/x-c++src", cc: "text/x-c++src", cxx: "text/x-c++src", hpp: "text/x-c++src",
  cs: "text/x-csharp",
  php: "php",
  sql: "text/x-sql",
  // web / config-adjacent
  css: "css", scss: "css", less: "css",
  html: "htmlmixed", htm: "htmlmixed",
  js: "javascript", mjs: "javascript", cjs: "javascript",
  ts: "application/typescript", tsx: "application/typescript", jsx: "javascript",
};

function resolveEditorMode(path) {
  if (!path) return null;
  const base = String(path).split("/").pop() || "";
  const lower = base.toLowerCase();
  if (Object.prototype.hasOwnProperty.call(BY_NAME, lower)) return BY_NAME[lower];
  const dot = lower.lastIndexOf(".");
  if (dot <= 0) return null; // no extension, or dotfile with no known whole-name match
  const ext = lower.slice(dot + 1);
  return Object.prototype.hasOwnProperty.call(BY_EXT, ext) ? BY_EXT[ext] : null;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { resolveEditorMode };
}
