# Ruk documentation site design

## Goal

Build a practical documentation site that teaches humans and coding agents how
to use Ruk safely. A new user should install Ruk and complete the
`acquire -> run -> release` workflow in five minutes.

The site will live in this repository and version with the CLI. It will publish
static files to GitHub Pages.

## Audience

The site serves two readers:

- humans operating coding agents, who need context, examples, and recovery
  guidance;
- agents consuming instructions directly, which need exact commands, JSON
  contracts, and explicit safety rules.

Each guide will explain the task before showing copyable commands. Pages for
automation will include expected JSON and identify values, such as assignment
IDs, that callers must retain.

## Information architecture

The public site will live under `website/`, separate from internal architecture,
plans, and research notes.

1. **Introduction**: purpose, use cases, and requirements.
2. **Getting started**: installation, first workspace, the core lifecycle, and
   status checks.
3. **Guides**: dependency modes, renewal, reuse, garbage collection, CI, and
   agent integration.
4. **Reference**: commands, configuration, environment variables, JSON output,
   and failure behavior.
5. **Troubleshooting**: dirty workspaces, preparation failures, expired
   assignments, branch conflicts, and package-manager compatibility.

The homepage will offer a primary **Get started** action and a secondary link to
the CLI reference. It will distinguish human and agent reading paths without
duplicating the underlying documentation.

## Technical design

VitePress will build the site from Markdown in `website/`. The root package will
provide these commands:

- `bun run docs:dev`
- `bun run docs:build`
- `bun run docs:preview`

VitePress local search will index the generated site. This avoids credentials
and external search services. The GitHub Pages base path will be `/ruk/`; a
future custom domain can change it to `/`.

A GitHub Actions workflow will build pull requests and deploy `main`. It will
use the repository's pinned Bun version and immutable action SHAs. Pull requests
will verify the build but never deploy it.

Documentation examples will derive from the CLI implementation, README, and
agent interface. A production build will validate configuration, links, and
page rendering. Browser checks will cover desktop and mobile layouts.

## Visual design

The site will extend VitePress's default accessible theme instead of replacing
it. Forest green will be the accent color. Light mode will use warm neutral
surfaces; dark mode will use charcoal green.

A small local tree or branch SVG will represent Ruk. The design will use
**Ruk** as the sole product name. Code examples will dominate the pages.
Warnings for assignment fencing and destructive commands will use clear
callouts.

The site will retain conventional documentation navigation: search, persistent
sidebar, page outline, previous and next links, and an edit-on-GitHub link.
Custom motion will be limited to focus and hover transitions and will respect
reduced-motion preferences.

## Initial exclusions

The first version will omit release versioning, analytics, a CMS, localization,
external search, and a custom component system. Add them only after a release or
measured demand makes their cost worthwhile.

## Verification

Before handoff:

1. build the production site with Bun;
2. run the repository's prescribed checks;
3. verify internal links and the GitHub Pages base path;
4. inspect the built site at desktop and mobile widths;
5. confirm pull requests build without receiving deployment permissions.
