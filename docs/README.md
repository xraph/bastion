# Bastion Documentation

Documentation site for [Bastion](https://github.com/xraph/bastion) — a production-grade API gateway extension for Forge.

Built with [Fumadocs](https://fumadocs.dev) and Next.js.

## Development

```bash
pnpm install
pnpm dev
```

Open http://localhost:3000 to preview.

## Structure

| Path | Description |
|------|-------------|
| `content/docs/` | MDX documentation pages |
| `content/docs/meta.json` | Top-level navigation |
| `app/(home)` | Landing page |
| `app/docs` | Documentation layout |
| `app/api/search/route.ts` | Search handler |
