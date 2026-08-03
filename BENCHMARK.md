# Large Bun monorepo: 20-workspace benchmark

Captured on 2026-08-02 from a large production Bun monorepo using
GitHub-hosted Ubuntu runners, Bun 1.3.14, the committed frozen lockfile, normal
lifecycle scripts, and twenty concurrent real Git worktrees. Repository and
package identifiers have been intentionally anonymized.

## Results

Two independent runs produced the following ranges:

| Dependency layout | Parallel wall time | Median install | Physical store + 20 workspaces | Logical local `node_modules` |
| --- | ---: | ---: | ---: | ---: |
| Bun isolated global store | 9.1–10.3 s | 8.7–10.1 s | ~1.62 GiB | ~461 MiB |
| Bun isolated local stores | 63.3–129.4 s | 62.7–128.9 s | ~2.65 GiB | ~19.82 GiB |

Every install completed successfully. Under 20-way contention, the global
store was approximately 7–12.5x faster, used 38.9% less physical disk, and
reduced logical per-workspace dependencies by approximately 97.7%.

## Compatibility finding

Successful installation did not mean the repository was compatible:

- Bun's hoisted layout passed the sampled application checks and builds.
- Isolated local mode exposed an undeclared transitive dependency in one
  workspace.
- Isolated global mode exposed that dependency plus additional TypeScript
  resolution failures.
- Both isolated modes conflicted with the repository's root-only
  `node_modules` policy.

This evidence is why Ruk defaults to managed mode. Shared mode must be enabled
only after the consuming repository certifies the complete dependency layout.
