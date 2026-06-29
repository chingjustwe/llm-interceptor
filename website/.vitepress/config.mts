import { defineConfig } from 'vitepress'

export default defineConfig({
  base: '/llm-interceptor/',
  lang: 'en-US',
  title: 'LLM Interceptor',
  description:
    'Local-first, open-source LLM gateway. Transparent proxy, OTel observability, governance, and multi-provider routing in a single Go binary.',
  head: [
    ['link', { rel: 'icon', href: '/llm-interceptor/logo.svg', type: 'image/svg+xml' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.googleapis.com' }],
    [
      'link',
      {
        rel: 'preconnect',
        href: 'https://fonts.gstatic.com',
        crossorigin: '',
      },
    ],
    [
      'link',
      {
        href: 'https://fonts.googleapis.com/css2?family=IBM+Plex+Sans:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500;600;700&display=swap',
        rel: 'stylesheet',
      },
    ],
  ],
  themeConfig: {
    logo: {
      src: '/logo.svg',
      width: 32,
      height: 32,
    },
    nav: [
      { text: 'Home', link: '/' },
      { text: 'Guide', link: '/guide/getting-started' },
      {
        text: 'GitHub',
        link: 'https://github.com/chingjustwe/llm-interceptor',
      },
    ],
    sidebar: {
      '/guide/': [
        {
          text: 'Guide',
          items: [
            {
              text: 'Getting Started',
              link: '/guide/getting-started',
            },
            { text: 'Configuration', link: '/guide/configuration' },
            {
              text: 'Supported Providers',
              link: '/guide/providers',
            },
            { text: 'Architecture', link: '/guide/architecture' },
            { text: 'Plugins', link: '/guide/plugins' },
            { text: 'Router Mode', link: '/guide/router-mode' },
            { text: 'Deployment', link: '/guide/deployment' },
            { text: 'Alerting', link: '/guide/alerting' },
            {
              text: 'API Reference',
              link: '/guide/api-reference',
            },
          ],
        },
      ],
    },
    socialLinks: [
      {
        icon: 'github',
        link: 'https://github.com/chingjustwe/llm-interceptor',
      },
    ],
    footer: {
      message: 'Released under the MIT License.',
      copyright: `Copyright © ${new Date().getFullYear()}`,
    },
  },
})
