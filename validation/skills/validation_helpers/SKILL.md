---
name: validation_helpers
description: Helper skill containing simple command tools for validation
---
Use this skill when a task explicitly asks for helper command tools during validation.

Available helper tools can inventory Markdown files and pretty-print JSON arguments.

The default Markdown inventory intentionally skips historical validation runs, generated reports, and temp/cache folders unless the task explicitly targets those paths.
