# LLM Interceptor Documentation Site

VitePress documentation site for the LLM Interceptor project. Published at `https://chingjustwe.github.io/llm-interceptor/`.

## Local Development

```bash
cd website
npm install
npm run dev
```

## Build

```bash
npm run build
```

Output goes to `website/.vitepress/dist/`.

## Directory Layout

```
website/
├── index.md              # Product landing page
├── .vitepress/
│   ├── config.mts         # VitePress configuration
│   └── theme/
│       ├── custom.css     # Design system (light/dark tokens)
│       ├── index.ts       # Theme entry, component registration
│       └── components/
│           ├── FlowDiagram.vue    # Animated pipeline visualization
│           └── TerminalHero.vue   # Terminal demo component
└── guide/                 # Documentation pages
    ├── getting-started.md
    ├── configuration.md
    ├── providers.md
    ├── architecture.md
    ├── plugins.md
    ├── router-mode.md
    ├── deployment.md
    ├── alerting.md
    └── api-reference.md
```

## Deployment

Pushing to `main` with changes under `website/**` triggers a GitHub Actions workflow that builds and deploys to GitHub Pages.
