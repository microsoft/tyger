import { defineConfig } from "vitepress";

const base = "/tyger";
// https://vitepress.dev/reference/site-config
export default defineConfig({
  title: "Tyger",
  description: "Tyger documentation",
  head: [["link", { rel: "icon", href: `${base}/favicon.ico` }]],
  themeConfig: {
    // https://vitepress.dev/reference/default-theme-config
    nav: [
      { text: "Home", link: "/" },
      { text: "Docs", link: "/introduction/what-is-tyger" },
    ],

    sidebar: [
      {
        text: "Introduction",
        items: [
          { text: "What is Tyger?", link: "/introduction/what-is-tyger" },
          { text: "Installation", link: "/introduction/installation" },
        ],
      },
      {
        text: "Guides",
        items: [
          { text: "Working with buffers", link: "/guides/buffers" },
          { text: "Working with codespecs", link: "/guides/codespecs" },
          { text: "Working with runs", link: "/guides/runs" },
          { text: "Distributed runs", link: "/guides/distributed" },
        ],
      },
      {
        text: "Reference",
        collapsed: true,
        items: [
          { text: "Installation configuration file", link: "/reference/config" },
          { text: "Database management", link: "/reference/database" },
          { text: "<code>tyger-proxy</code>", link: "/reference/tyger-proxy" },
        ],
      },
    ],

    socialLinks: [
      { icon: "github", link: "https://github.com/microsoft/tyger" },
    ],

    search: {
      provider: "local",
    },

    outline: {
      level: "deep",
    },
  },

  base: base,
  srcExclude: ["README.md"],
});
