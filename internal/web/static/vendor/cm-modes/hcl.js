"use strict";
// HCL / Terraform highlighting — CodeMirror 5 has no upstream HCL mode, so this
// is a compact simple-mode (readable highlighting, not a full parser). Loaded
// after the official addon/cm-addon/simple.js.
(function () {
  if (!window.CodeMirror || !CodeMirror.defineSimpleMode) return;
  CodeMirror.defineSimpleMode("hcl", {
    start: [
      { regex: /#.*|\/\/.*/, token: "comment" },
      { regex: /\/\*/, token: "comment", next: "comment" },
      { regex: /"(?:[^\\"]|\\.)*"/, token: "string" },
      { regex: /\b(?:true|false|null)\b/, token: "atom" },
      { regex: /-?(?:\d+\.?\d*|\.\d+)/, token: "number" },
      { regex: /\b(?:resource|variable|module|provider|data|output|locals|terraform|dynamic|for_each|count|depends_on|lifecycle|provisioner|backend)\b/, token: "keyword" },
      { regex: /[A-Za-z_][\w-]*(?=\s*=)/, token: "property" },
      { regex: /\$\{/, token: "meta", next: "interp" },
      { regex: /[A-Za-z_][\w.-]*/, token: "variable" },
    ],
    comment: [
      { regex: /.*?\*\//, token: "comment", next: "start" },
      { regex: /.*/, token: "comment" },
    ],
    interp: [
      { regex: /\}/, token: "meta", next: "start" },
      { regex: /[^}]+/, token: "variable-2" },
    ],
    meta: { lineComment: "#" },
  });
  CodeMirror.defineMIME("text/x-hcl", "hcl");
})();
