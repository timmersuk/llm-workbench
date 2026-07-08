# AGENTS.md

This repository implements the LLM Workbench.

Before making significant changes, read:

- docs/project-summary.md
- docs/architectural-invariants.md
- docs/engineering-conventions.md

General rules:

- Preserve architectural invariants unless explicitly instructed.
- Prefer extending existing abstractions over introducing new ones.
- Keep provider interfaces narrow and replaceable.
- Run tests before considering work complete.
- Update documentation when architecture changes.