import { defineConfig } from "vitepress";

export default defineConfig({
  title: "Ruk",
  titleTemplate: ":title · Ruk",
  description: "Dependency-aware Git workspaces for parallel coding agents",
  lang: "en-US",
  base: process.env["DOCS_BASE"] ?? "/ruk/",
  cleanUrls: true,
  lastUpdated: true,
  head: [
    ["meta", { name: "theme-color", content: "#176b47" }],
    ["meta", { name: "color-scheme", content: "light dark" }],
  ],
  markdown: {
    lineNumbers: true,
  },
  themeConfig: {
    siteTitle: "Ruk",
    nav: [
      { text: "Guide", link: "/getting-started/" },
      { text: "Agent use", link: "/guides/agent-integration" },
      { text: "Skills", link: "/skills/" },
      { text: "Reference", link: "/reference/cli" },
    ],
    sidebar: [
      {
        text: "Introduction",
        items: [
          { text: "What is Ruk?", link: "/" },
          { text: "Install", link: "/getting-started/install" },
          { text: "First workspace", link: "/getting-started/" },
        ],
      },
      {
        text: "Guides",
        items: [
          { text: "Agent integration", link: "/guides/agent-integration" },
          { text: "Dependency modes", link: "/guides/dependency-modes" },
          { text: "Assignments and renewal", link: "/guides/assignments" },
          { text: "Garbage collection", link: "/guides/garbage-collection" },
        ],
      },
      {
        text: "Skills",
        items: [{ text: "Ruk workspace skill", link: "/skills/" }],
      },
      {
        text: "Reference",
        items: [
          { text: "CLI", link: "/reference/cli" },
          { text: "Configuration", link: "/reference/configuration" },
          { text: "JSON contracts", link: "/reference/json" },
        ],
      },
      {
        text: "Help",
        items: [{ text: "Troubleshooting", link: "/troubleshooting" }],
      },
    ],
    search: {
      provider: "local",
    },
    outline: {
      level: [2, 3],
      label: "On this page",
    },
    editLink: {
      pattern: "https://github.com/xenoviz/ruk/edit/main/website/:path",
      text: "Edit this page on GitHub",
    },
    socialLinks: [{ icon: "github", link: "https://github.com/xenoviz/ruk" }],
    footer: {
      message: "Local workspaces. Explicit ownership. No background service.",
      copyright: "Released under the MIT License.",
    },
    docFooter: {
      prev: "Previous",
      next: "Next",
    },
  },
});
