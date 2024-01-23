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
      { text: "Guide", link: "/guide/introduction" },
    ],

    sidebar: [
      {
        text: "Introduction",
        items: [
          { text: "What is Tyger?", link: "/guide/what-is-tyger" },
          { text: "Installation", link: "/guide/installation" },
          { text: "Hello world!", link: "/guide/hello-world" },
        ],
      },
      {
        text: "How-to guides",
        items: [
          { text: "Create a buffer", link: "/guide/a" },
          { text: "Create a run", link: "/guide/a" },
          { text: "Track runs", link: "/guide/a" },
          { text: "Resource management", link: "/guide/a" },
          { text: "Distributed runs", link: "/guide/a" },
        ],
      },
      {
        text: "Reference",
        // collapsed: false,
        items: [
          {
            text: "Core commands",
            collapsed: true,
            items: [
              { text: "<code>tyger login</code>", link: "/guide/a" },
              {
                text: "<code>tyger buffer</code>",
                collapsed: true,
                items: [
                  { text: "<code>create</code>", link: "/guide/a" },
                  { text: "<code>access</code>", link: "/guide/a" },
                  { text: "<code>read</code>", link: "/guide/a" },
                  { text: "<code>write</code>", link: "/guide/a" },
                  { text: "<code>show</code>", link: "/guide/a" },
                  { text: "<code>set</code>", link: "/guide/a" },
                  { text: "<code>list</code>", link: "/guide/a" },
                ],
              },
              {
                text: "<code>tyger codespec</code>",
                collapsed: true,
                items: [
                  { text: "<code>create</code>", link: "/guide/a" },
                  { text: "<code>show</code>", link: "/guide/a" },
                  { text: "<code>list</code>", link: "/guide/a" },
                ],
              },
              {
                text: "<code>tyger run</code>",
                collapsed: true,
                items: [
                  { text: "<code>create</code>", link: "/guide/a" },
                  { text: "<code>exec</code>", link: "/guide/a" },
                  { text: "<code>show</code>", link: "/guide/a" },
                  { text: "<code>watch</code>", link: "/guide/a" },
                  { text: "<code>logs</code>", link: "/guide/a" },
                  { text: "<code>list</code>", link: "/guide/a" },
                ],
              },
            ],
          },
          {
            text: "Installation commands",
            collapsed: true,
            items: [
              {
                text: "<code>tyger login</code>",
                collapsed: true,
                items: [
                  { text: "<code>create</code>", link: "/guide/a" },
                  { text: "<code>get-path</code>", link: "/guide/a" },
                ],
              },
              {
                text: "<code>tyger config</code>",
                collapsed: true,
                items: [
                  { text: "<code>create</code>", link: "/guide/a" },
                  { text: "<code>get-path</code>", link: "/guide/a" },
                ],
              },
              {
                text: "<code>tyger cloud</code>",
                collapsed: true,
                items: [
                  { text: "<code>install</code>", link: "/guide/a" },
                  { text: "<code>uninstall</code>", link: "/guide/a" },
                ],
              },
              {
                text: "<code>tyger api</code>",
                collapsed: true,
                items: [
                  { text: "<code>install</code>", link: "/guide/a" },
                  { text: "<code>uninstall</code>", link: "/guide/a" },
                  {
                    text: "<code>migration</code>",
                    collapsed: true,
                    items: [
                      { text: "<code>list</code>", link: "/guide/a" },
                      { text: "<code>apply</code>", link: "/guide/a" },
                      { text: "<code>logs</code>", link: "/guide/a" },
                    ],
                  },
                ],
              },
            ],
          },
          { text: "<code>tyger-proxy</code>", link: "/guide/a" },
        ],
      },
    ],

    socialLinks: [
      { icon: "github", link: "https://github.com/microsoft/tyger" },
    ],

    search: {
      provider: "local",
    },
  },

  base: base,
  srcExclude: ["README.md"],
});
