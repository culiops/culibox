"use strict";

// Pure filename -> Monaco language id, for the dock's editor. The CodeMirror 5
// resolver (editmode.js) stays for the phone's ViewerTab; this is its Monaco
// twin, covering the same set of files so a file that was highlighted before is
// still highlighted. Kept free of Monaco and the DOM so `node --test` reaches it.
//
// The default is "plaintext", Monaco's own id for no highlighting. Monaco ships
// no makefile, cmake, groovy or nginx tokenizer, so those are plaintext on
// purpose rather than mapped to a near miss that would colour them wrongly.

// By whole lowercased filename (no extension, or the name is the signal).
const ML_BY_NAME = {
  "dockerfile": "dockerfile",
  "containerfile": "dockerfile",
  "jenkinsfile": "plaintext",
  "makefile": "plaintext",
  "gnumakefile": "plaintext",
  "vagrantfile": "ruby",
  "gemfile": "ruby",
  "rakefile": "ruby",
  "berksfile": "ruby",
  "brewfile": "ruby",
  ".env": "ini",
  ".bashrc": "shell",
  ".bash_profile": "shell",
  ".zshrc": "shell",
  ".profile": "shell",
  ".gitconfig": "ini",
};

// By lowercased extension.
const ML_BY_EXT = {
  // IaC / config / data
  tf: "hcl", tfvars: "hcl", hcl: "hcl",
  yaml: "yaml", yml: "yaml",
  json: "json",
  toml: "ini", ini: "ini", conf: "ini", cfg: "ini", properties: "ini", env: "ini",
  nginx: "plaintext",
  xml: "xml", xsd: "xml", plist: "xml", svg: "xml",
  md: "markdown", markdown: "markdown",
  dockerfile: "dockerfile",
  mk: "plaintext", make: "plaintext", cmake: "plaintext",
  // shells / scripting
  sh: "shell", bash: "shell", zsh: "shell", ksh: "shell",
  py: "python", pyw: "python",
  rb: "ruby",
  pl: "perl", pm: "perl",
  lua: "lua",
  ps1: "powershell", psm1: "powershell", psd1: "powershell",
  groovy: "plaintext", gradle: "plaintext",
  // compiled / platform code
  go: "go",
  rs: "rust",
  java: "java", kt: "kotlin", scala: "scala",
  c: "c", h: "c",
  cpp: "cpp", cc: "cpp", cxx: "cpp", hpp: "cpp",
  cs: "csharp",
  php: "php",
  sql: "sql",
  // web
  css: "css", scss: "scss", less: "less",
  html: "html", htm: "html",
  js: "javascript", mjs: "javascript", cjs: "javascript", jsx: "javascript",
  ts: "typescript", tsx: "typescript",
};

function monacoLang(path) {
  if (!path) return "plaintext";
  const lower = (String(path).split("/").pop() || "").toLowerCase();
  if (Object.prototype.hasOwnProperty.call(ML_BY_NAME, lower)) return ML_BY_NAME[lower];
  const dot = lower.lastIndexOf(".");
  if (dot <= 0) return "plaintext"; // no extension, or a dotfile with no known whole-name match
  const ext = lower.slice(dot + 1);
  return Object.prototype.hasOwnProperty.call(ML_BY_EXT, ext) ? ML_BY_EXT[ext] : "plaintext";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { monacoLang };
}
