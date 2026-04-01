---
name: review_pipeline
description: Findings-first review workflow with scoped evidence and confidence labels
---
When asked to review or audit code:

1. Start by fixing scope: name the allowed directories or files before reading deeply.
2. Keep findings first, ordered by severity, unless the task explicitly requires a different opening template or section order. In that case, the task-specific ordering wins.
3. For each finding, record four fields in the prose or report body: severity, confidence, evidence, and why it matters.
4. Treat behavior claims separately from declaration-level hints. If behavior is not proven, keep the point under risk, inference, or unresolved questions.
5. Keep unresolved questions and follow-up reads separate from validated findings.
6. If a report is requested, use a canonical structure when possible: findings, unresolved questions, smallest fixes or next steps.
7. In the findings section, prefer one record per finding with explicit `Severity:`, `Confidence:`, `Evidence:`, and `Why it matters:` labels so runtime validators can recognize the artifact as complete. If there are no validated findings, write the exact sentence `No validated findings.` inside the findings section.
8. In each finding, include concrete `path:line` references plus a short quoted snippet or identifier from those cited lines, either inline in `Evidence:` or in a separate `Snippet:` field, so the runtime validator can verify the evidence is both readable and grounded.
9. If the task requires literal anchor tokens, exact sentences, exact section headings, or a required opening template, reproduce them verbatim in the requested position. Do not paraphrase, normalize, rename, bullet, or reorder those required strings.
