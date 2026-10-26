// Documentation site. The content is the repository's docs/ directory as is:
// this project only renders it, so documents are still written and reviewed
// next to the code.
import type { Config } from "@docusaurus/types";
import type * as Preset from "@docusaurus/preset-classic";
import { themes as prismThemes } from "prism-react-renderer";

const config: Config = {
  title: "О!дин запрос",
  tagline: "Документация проекта",
  url: process.env.DOCS_URL ?? "http://localhost",
  // Served by Caddy under /docs/ next to the application.
  baseUrl: "/docs/",

  // Some documents link to code outside docs/ (README, deploy/), which the
  // site does not publish. Those links are reported, not fatal.
  onBrokenLinks: "warn",
  markdown: {
    // .md files are plain CommonMark, so text like "<5 минут" or "{id}" in the
    // documents does not have to be valid MDX.
    format: "detect",
    hooks: { onBrokenMarkdownLinks: "warn" },
  },

  i18n: { defaultLocale: "ru", locales: ["ru"] },

  presets: [
    [
      "classic",
      {
        docs: {
          path: "../docs",
          routeBasePath: "/",
          sidebarPath: "./sidebars.ts",
          // Keep "0001-architecture" in URLs, the same names as in the repository.
          numberPrefixParser: false,
        },
        blog: false,
        theme: { customCss: "./src/css/custom.css" },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    navbar: {
      title: "О!дин запрос",
      items: [{ type: "docSidebar", sidebarId: "docs", label: "Документация", position: "left" }],
    },
    colorMode: { respectPrefersColorScheme: true },
    prism: { theme: prismThemes.github, darkTheme: prismThemes.dracula, additionalLanguages: ["bash", "go", "protobuf", "yaml"] },
  } satisfies Preset.ThemeConfig,
};

export default config;
