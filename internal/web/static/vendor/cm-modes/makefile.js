"use strict";
// Makefile highlighting — no upstream CM5 mode; compact simple-mode.
(function () {
  if (!window.CodeMirror || !CodeMirror.defineSimpleMode) return;
  CodeMirror.defineSimpleMode("makefile", {
    start: [
      { regex: /#.*/, token: "comment" },
      { regex: /^[^\s:#=][^:#=]*(?=:(?!=))/, token: "def" },
      { regex: /^\t.*/, token: "string" },
      { regex: /\$[({][^)}]*[)}]/, token: "variable-2" },
      { regex: /\b[A-Za-z_][A-Za-z0-9_]*(?=\s*[:?+!]?=)/, token: "variable" },
      { regex: /\b(?:ifeq|ifneq|ifdef|ifndef|else|endif|include|-include|define|endef|export|unexport|override|vpath)\b/, token: "keyword" },
      { regex: /\.[A-Z]+\b/, token: "atom" },
    ],
    meta: { lineComment: "#" },
  });
})();
