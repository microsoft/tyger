# Documentation Website

This site uses [VitePress](https://vitepress.dev/) to generate a static site for
documentation. To run it locally, run

```bash
$ make start-docs-website
```

In most cases, the page in the browser should update when saving the markdown
files. But changes to `.vitepress/config.mts` require a page reload.

VitePress 1.x depends on Vite 5, which no longer receives the security fixes we
need. The version-scoped override in `package.json` replaces Vite 5 with patched
Vite 6 for VitePress without changing the top-level Vite dependency. The esbuild
override also ensures a patched version is selected from Vite's supported ranges.
Keep both Vite versions patched and run `npm audit` and `npm run docs:build`
after dependency updates.
