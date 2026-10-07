# Dashboard migration: Go-rendered pages to React shell

Bastion's dashboard used to render server-side from `bastion/dashboard/`, as a
`contributor.LocalContributor`. It now lives in the Forge dashboard's React shell
as `packages/plugin-bastion` (in the `forge-dashboard` repo), reading the
`bastion` contract contributor in `extension/contract`. The Go directory is gone.

This file is the record of that move. We wrote it by reading every line of the
old dashboard before deleting it: `contributor.go`, `manifest.go`, `data.go`,
`render_pages.go`, `render_widgets.go` and `shared/types.go`, 1,148 lines in all.
That is nine pages, three widgets and one settings panel. Once the directory is
gone nothing is left to check against, so each page, column, stat, badge, empty
state, widget and action is listed below with what happened to it.

The design behind the move is in `forge-dashboard`, at
`docs/superpowers/specs/2026-09-30-bastion-dashboard-migration-design.md`.

Status is one of:

- Migrated: it exists in the React plugin today. The row says where, and which
  contract intent feeds it.
- Dropped: gone on purpose, with the reason.

Every old page now has a React home. The last six (traffic, health, circuits,
services, the API explorer and config) landed in the last slice, so the plugin's
sidebar has nine entries, the same nine the old dashboard had. Nothing is left to do.
The admin REST API is unchanged apart from what the REST section below lists.

## There was no templ in the templ dashboard

`dashboard/` held no `.templ` files. Every page was HTML built with
`fmt.Sprintf` and wrapped in `templ.ComponentFunc`, and nothing was escaped.
Route paths, service names, versions, addresses and upstream spec errors went
into the markup raw (`render_pages.go:443` to `render_pages.go:638`, and the
OpenAPI error text at `render_pages.go:262`). FARP push registration has no auth
(see Still open), so anyone who could reach the gateway could store markup that
the dashboard then rendered for an operator. Deleting the directory closes that.
`github.com/a-h/templ` was imported only by `dashboard/contributor.go` and leaves
`go.mod` with it.

## What you need to do

If you ran the old dashboard through `DashboardAware`, you don't need to do
anything. After the deletion the extension stops implementing `DashboardAware`
(it did at `extension/extension.go:33` and `extension/extension.go:233`) and
registers the contract contributor through `ContractContributorAware`. The shell
finds it. If your own code imports `github.com/xraph/bastion/dashboard`, that
import has to go, because the package no longer exists.

Add the plugin to your shell:

```tsx
import bastionPlugin from "@forge-go/dashboard-plugin-bastion"

const plugins = [corePlugin, bastionPlugin]
```

The plugin's pages use Tailwind classes of their own, so the shell's stylesheet
has to scan the package. If your shell declares its sources with `@source`, add
`@source "<path to>/packages/plugin-bastion/src";` next to the others.

Bastion now needs forge v1.12.0. That release removes the dashboard
`contributor` package and `DashboardAware` along with templ, which is why the old
dashboard had to go, and it lets `go mod tidy` drop `a-h/templ` and `forgeui`.

`DashboardConfig` stays. `Dashboard.Enabled` also gates the admin REST API and the
WebSocket hub, and `Dashboard.BasePath` prefixes the OpenAPI endpoints, so
removing it is a separate decision. Its doc comments are corrected to say what it
actually gates.

Until `pnpm-lock.yaml` in `forge-dashboard` carries the `plugin-bastion` importer
(see Still open), run `pnpm install` without `--frozen-lockfile` the first time.

## Bugs found on the way

Reading the old pages closely enough to rebuild them turned up numbers that were
never real. All of these are fixed in the Go core, so the React pages show
something bastion actually measures, or say "Not measured".

- Average latency was always 0.0 ms. `StatsCollector.Snapshot` never set
  `AvgLatencyMs` (the overview had it, the traffic page's "Avg Latency" column and
  the stats widget showed it). Per-request duration is now recorded gateway-wide
  and per route, with a bounded p99.
- Cache hit rate was always 0.0%. `RecordCacheHit` and `RecordCacheMiss` had no
  callers. The counts now come from the response cache's own counters, and the
  contract answers `null` ("Not measured") when there were no lookups. See Still
  open: the cache never stores, so with caching on you will see a real 0.0% over
  real lookups.
- Every circuit read "closed". `Target.CircuitState` was never assigned
  (`render_pages.go:604` printed "closed" for an empty value, which was always).
  The real state lives in `resilience.CircuitBreaker`. `Gateway.Circuits()` and
  `Gateway.ResetCircuit(targetID)` now expose it, and `Target.CircuitState` is
  filled only on copies.
- A breaker that opened never closed again. `RecordSuccess` was never called, so a
  half-open breaker let `HalfOpenMax` probes through and then refused forever,
  until a restart. A proxied answer below 500 now records a success.
- Uptime counted from the collector's construction at Register, not from Start,
  and the overview printed it as raw seconds ("3600s"). It counts from Start now,
  and the page prints "1h 0m".
- "Healthy / Total upstreams" counted per-route target entries, so a target shared
  by two routes counted twice. It counts distinct URLs now, and a URL is healthy
  only if every entry for it is.
- `RouteStats` for a deleted route were never removed, so the traffic table kept
  ghosts. They are dropped with the route.
- The OpenAPI refresh button (and the REST refresh) ran
  `go oa.Refresh(ctx.Request().Context())`, so the refresh was cancelled the
  moment the handler returned. The explorer's "Refresh Specs" reloaded the page
  while a cancelled refresh was still running. It now runs on a detached context.
- The old pages read targets through `Target.Snapshot()`, which writes exported
  fields on shared live targets while the proxy reads them. The health and
  circuit pages called it once per row (`render_pages.go:581`, `:610`). Every
  contract handler copies instead. See Still open for the callers that remain.
- The error widget promised "Error percentage and open circuit count" and never
  showed an open circuit count. It showed "Circuit Breaks", a running total of
  rejected requests, which is a different number. The overview now shows open
  circuits, with the half-open count under it, for the first time.

## What changed for REST clients of the admin API

The React dashboard and the REST handlers now share one write path, the
`bastion/admin` service. That fixed real bugs, and it also changes what the REST
API answers. The response shapes are the same. If you script against the admin API,
read this section.

- Create validates its input and answers 400 with a `field` naming the problem. It
  refuses a path that doesn't start with `/`, a route with no upstream, an
  upstream URL that isn't `http`, `https`, `ws` or `wss`, a duplicate upstream URL
  on one route, a negative weight, an unknown protocol or method, a rate limit
  without `requestsPerSec` above 0 and a `burst` of at least 1, and a masked
  password (`xxxxx`) in an upstream URL. Before, most of these were stored and
  failed later, or never.
- A second manual route on the same path with an overlapping method answers 409,
  with an `error` that names the other route. (The dashboard contract answers the
  same refusal as CONFLICT with `details.reason` `duplicate` and `routeId`; REST
  bodies carry only `error`, plus `field` on a 400.) Shadowing a discovered route
  is still allowed.
- Enable and disable on a FARP or discovery route answer 400. They used to
  succeed, and the next discovery update reverted them.
- Enable and disable now write an access-log admin line, like the other writes.
- Update decodes the body before it looks the route up. A bad body sent to a
  missing id now answers 400, not 404.
- Target ids for routes created over REST are `<routeId>/<n>`. They were
  `target-<scheme>-<host>`, which meant two routes sharing an upstream shared one
  health entry and one breaker, and deleting either route deregistered the target
  for both. If you stored those ids anywhere, they are different now.
- The REST refresh of the OpenAPI aggregate is no longer cancelled when its
  handler returns.

One drift is still there, and you should plan around it. GET returns the
effective priority and path (the +100 manual offset added, the
`Dashboard.BasePath` prefixed), while PUT takes the values as entered. A REST
client that PUTs back exactly what it GETs still moves the route up another 100
and double-prefixes its path, on every round trip. The dashboard doesn't have this
problem: `routes.detail` carries both values (`priority` and `input.priority`,
`path` and `input.path`), and the editor sends the `input` ones.

## Deliberately dropped

- The three widgets `bastion-stats`, `bastion-health` and `bastion-errors`. The
  React shell's own overview replaces them, and nothing else mounts a widget.
  What each one showed is in the Widgets section below.
- The settings panel `bastion-config`. `config.detail` covers every field in it,
  and the Config page shows them.
- The Swagger UI iframe on the API explorer. It pointed at `BasePath` plus
  `UIPath` (`render_pages.go:242` and `:327`), a route registered only when
  `EnableGatewayDocs` is on, which is off by default, and even then it served the
  admin API's own spec, not the aggregated upstream spec
  (`extension/extension.go:510` to `:517`). By default it was a 404 inside a
  800 pixel box. The explorer page is a spec summary with refresh and links.
- "Recent Routes" on the overview. It was the first five routes by priority, not
  by recency, and showed only a path and an Active or Disabled badge. The overview
  now lists the busiest routes by request count, which is what somebody opening
  it wants.
- The Overview's "Route Summary" card. It repeated three of the four stat cards
  (routes, upstreams) next to cache hit rate and uptime, which moved into the stat
  grid.
- The topbar title "Bastion Gateway", the shield logo and the `#6366f1` accent,
  and `ShowSearch: false`. The shell themes every plugin the same way.
- Layout `extension` and `ShowSidebar: true`. The shell decides layout.
- Manifest version `1.0.0`. The plugin has its own package version.
- The `emptyState` helper behind "Page Not Found", "Unknown Widget" and "Unknown
  Setting" (`contributor.go:59`, `:73`, `:83`, `:214`). The shell's router has its
  own not-found page, and there are no widgets or settings panels left to be
  unknown.
- `shared.PaginationMeta` and `NewPaginationMeta`. Defined, never called. Bastion's
  lists return the whole set with a total, because the route table is in memory
  and bounded.
- `GatewayOverview` (`shared/types.go:4`), the struct behind the overview.
  `overview.stats` carries its fields, and more.
- The icons passed to `statCard` (route, server, activity, alert-circle,
  alert-triangle, zap, shield-off, check-circle, file-text, clock). `statCard`
  never rendered its `icon` argument, so nothing visible is lost. The nav icons
  moved over.

## Page by page

Every old page rendered a title, a subtitle and then its content, with no filters,
no paging and no actions beyond one refresh button on the explorer. Where an old
empty state was a table row ("No routes configured") the new one is the kit's
empty state with a sentence that says what to do next.

### Route map

The old routes were the nine sidebar paths. The new ones are scope-relative
inside the plugin.

| old route | React route | status |
|---|---|---|
| `/` | `/` | Migrated: `BastionOverviewPage` |
| `/routes` | `/routes` | Migrated: `BastionRoutesPage` |
| none | `/routes/:id` | Migrated, and new: route detail. The id is percent-encoded in the URL, because config route ids (`manual-<path>`) contain `/` |
| none | `/new-route` | Migrated, and new: create. Never `/routes/new`, so no route id can shadow it |
| none | `/routes/:id/edit` | Migrated, and new: edit a manual route |
| `/upstreams` | `/upstreams` | Migrated: `BastionUpstreamsPage` |
| `/services` | `/services` | Migrated: `BastionServicesPage`, `services.list` and `discovery.refresh` |
| `/traffic` | `/traffic` | Migrated: `BastionTrafficPage`, `traffic.stats` |
| `/health` | `/health` | Migrated: `BastionHealthPage`, `upstreams.list` and `config.detail` |
| `/circuits` | `/circuits` | Migrated: `BastionCircuitsPage`, `circuits.list` and `circuits.reset` |
| `/api-explorer` | `/api-explorer` | Migrated: `BastionApiExplorerPage`, `openapi.summary` and `openapi.refresh` |
| `/config` | `/config` | Migrated: `BastionConfigPage`, `config.detail` |

An unknown route used to render a "Page Not Found" card with the route in it. The
shell answers that now.

### Navigation and manifest

`manifest.go:30` to `manifest.go:42`, plus the descriptor at `manifest.go:10`.

| old | React | status |
|---|---|---|
| Nav Overview, `/`, icon layout-dashboard, group Gateway, priority 0 | Overview, `/`, group Gateway, priority 0 | Migrated |
| Nav Routes, `/routes`, icon route, group Routing, priority 10 | Routes, group Routing, priority 10 | Migrated |
| Nav Upstreams, `/upstreams`, icon server, group Routing, priority 11 | Upstreams, group Routing, priority 11 | Migrated |
| Nav Services, `/services`, icon network, group Routing, priority 12 | Services, group Routing, priority 12 | Migrated: `services.list` |
| Nav Traffic, `/traffic`, icon activity, group Traffic, priority 20 | Traffic, group Traffic, priority 20 | Migrated: `traffic.stats` |
| Nav Health, `/health`, icon heart-pulse, group Resilience, priority 30 | Health, group Resilience, priority 30 | Migrated: `upstreams.list` and `config.detail` |
| Nav Circuits, `/circuits`, icon toggle-left, group Resilience, priority 31 | Circuits, group Resilience, priority 31 | Migrated: `circuits.list` |
| Nav API Explorer, `/api-explorer`, icon globe, group API, priority 40 | API explorer, group API, priority 40, code icon | Migrated: `openapi.summary` |
| Nav Config, `/config`, icon settings, group Settings, priority 50 | Config, group Settings, priority 50 | Migrated: `config.detail` |
| Display name "Bastion", icon shield | plugin label "Bastion", shield icon | Migrated |
| Topbar title "Bastion Gateway", logo icon, accent `#6366f1`, no search | none | Dropped: the shell themes plugins alike |
| Layout "extension", sidebar shown, version "1.0.0" | none | Dropped: the shell decides |
| Three widgets and one settings descriptor | none | Dropped: see Widgets and Config settings panel |

The new plugin adds no nav entry for route detail, create or edit. A sidebar link
to "a route" with none chosen points nowhere.

### Overview

`renderOverviewPage`, `render_pages.go:14`, fed by `fetchOverview`, `fetchStats`
and `fetchRoutes` (`data.go`). React: `pages/overview.tsx`, intent
`overview.stats`, polled.

| old | React | status |
|---|---|---|
| Title "Overview", subtitle "Bastion API Gateway dashboard" | Title "Gateway", "Traffic, upstream health and circuit state for this gateway process." | Migrated |
| Stat card "Routes", the route count, "Active routes" (the count included disabled routes) | "Routes", "N of M", hint "enabled" | Migrated: it now says how many are enabled |
| Stat card "Upstreams", "healthy/total", "Healthy/Total" | "Upstreams", "N of M", hint "healthy" | Migrated: counted by distinct URL, see Bugs found on the way |
| Stat card "Requests", total, "Total requests", abbreviated "1.2K" and "3.4M" | "Requests", full count with separators, hint "N errors" | Migrated |
| Stat card "Error Rate", "0.0%" with no requests, "Current error rate" | "Error rate", one decimal, or "Not measured" with the hint "No requests yet" | Migrated: no requests is no rate, not 0% |
| Card "Route Summary", row "Total Routes" | the "Routes" stat | Migrated |
| Row "Healthy Upstreams", "n / m" | the "Upstreams" stat | Migrated |
| Row "Cache Hit Rate", always "0.0%" | "Cache hit rate": a percentage and "N lookups", or "Not measured" with "No cache lookups" | Migrated: now measured, see Bugs found on the way |
| Row "Uptime", raw seconds, counted from Register | "Uptime": "3d 4h", "2h 5m", counted from Start, or "Not started" | Migrated |
| Card "Recent Routes", the first five routes by priority, path and Active or Disabled badge | "Busiest routes" table: path (a link to the route), requests, errors, at most five | Dropped as it was, replaced: see Deliberately dropped |
| Empty "No routes configured" | "No route has served traffic yet." | Migrated |
| No latency figure on the overview (only the widget had one) | "Latency": average, with "p99 X over the last N responses", or "Not measured" with "No upstream has answered yet" | Migrated, new |
| No open-circuit figure anywhere (the error widget promised one) | "Open circuits", with "N half-open", or "Off" when circuit breaking is disabled | Migrated, new |
| No error on a failed read; a handler could not fail | A failed query shows an error panel | Migrated, new |

The overview polls, so the numbers move without a reload. `overview.stats` also
returns `rateLimited`, `circuitBreaks`, `discoveryEnabled` and a latency sample
count. The page uses the sample count and leaves the rest for the traffic page.

### Routes

`renderRoutesPage` and `renderRouteRows`, `render_pages.go:52` and `:474`. React:
`pages/routes.tsx`, intent `routes.list`.

| old | React | status |
|---|---|---|
| Title "Routes", subtitle "N configured routes" (the count of all routes) | Title "Routes", the subtitle describes the list as manual routes and discovered ones, and the table caption has the count | Migrated |
| Column "Path", mono, plain text | "Path", mono, a link to the route's page | Migrated |
| Column "Methods", the Go slice printed as `[GET POST]`, or "-" when empty | "Methods", one tag per method, or "Any" when empty | Migrated |
| Column "Protocol", plain text | "Protocol", an outline badge in mono | Migrated |
| Column "Targets", the target count | "Upstreams", "healthy/total healthy" | Migrated: it now says how many are up |
| Column "Source", plain text | "Source", an outline badge: Manual, FARP, Discovery | Migrated |
| Column "Status", a green "Active" or grey "Disabled" pill | "Status", an outline "Enabled" badge or a secondary "Disabled" one | Migrated |
| No priority column | "Priority", mono, right-aligned (the effective value) | Migrated, new |
| No filters | Source and Protocol selects (the five protocols: http, websocket, sse, grpc, graphql). An unset filter is left out of the request | Migrated, new |
| No actions | "New route" and "Refresh discovery" buttons | Migrated, new: see Actions |
| Empty "No routes configured", a table row | "No routes. Create one, or let discovery find your services." with a "New route" button, or "No routes match these filters." under a filter | Migrated: the two cases are different sentences |

The old page listed routes in the route manager's order. The new one lists them in
the order `routes.list` returns them.

### Route detail, create and edit (new)

The old dashboard had no route page and no way to change anything. These come from
the admin API's existing operations. React: `pages/route-detail.tsx`,
`pages/route-create.tsx`, `pages/route-edit.tsx`, `components/route-form.tsx`;
intents `routes.detail`, `routes.create`, `routes.update`, `routes.delete`,
`routes.setEnabled`.

| item | status |
|---|---|
| Detail header: path as title, "Served by N upstreams" | Migrated, new |
| Detail actions for a manual route: Edit, Disable (confirmed) or Enable, Delete (confirmed). A discovered or FARP route shows none | Migrated, new |
| Detail facts: Route ID, Source (with "from the config file" for config routes), Service, Protocol, Methods, Priority ("N (entered as M)"), Status, Strip prefix, Add prefix, Rewrite path, Metadata keys, Updated | Migrated, new |
| Targets table: URL, Weight, Health, Circuit, Requests, Errors, Avg latency (blank until the target has a request) | Migrated, new |
| Headers table: where (Set, Add, Remove, and the request and response transform variants), header name, value. A value the server redacted shows a "Redacted" badge. A removed header shows "Removed" | Migrated, new |
| Overrides: retry, timeout, rate limit, auth, circuit breaker, cache and traffic policy, as stored. Retry, timeout, circuit breaker and cache carry a "Not applied" badge, because the proxy never reads them | Migrated, new |
| Notice for a config route: the change lasts until the gateway restarts | Migrated, new |
| Notice for a discovered route: discovery replaces any change | Migrated, new |
| Editor fields: path, methods (seven checkboxes), protocol, priority, upstreams (URL, weight, tags, add and remove), strip prefix, add prefix, rewrite path, rate limit (on, requests per second, burst, per client), authentication (on, providers, scopes, skip gateway authentication, forward identity headers), enabled | Migrated, new |
| Editor behaviour: path and priority are shown as you entered them, and the form says Bastion adds 100 to manual priorities. A masked upstream password comes back unchanged and the server keeps the stored one. Headers, transforms, traffic policy and metadata are kept as stored. A rate limit or auth override stored as disabled stays disabled | Migrated, new |
| Refusals: a field the server names (`path`, `methods`, `protocol`, `targets`, `rateLimit`, `auth`) is shown beside that field. A duplicate path answers CONFLICT with a link to the clashing route. Anything else appears in an alert | Migrated, new |

The editor offers only the overrides the proxy applies, rate limit and auth.
Timeout, retry, per-route breaker and cache overrides are kept as stored on every
save and marked "Not applied" on the detail page. Traffic policies and transforms
are read-only in the dashboard.

### Upstreams

`renderUpstreamsPage` and `renderUpstreamRows`, `render_pages.go:79` and `:505`.
React: `pages/upstreams.tsx`, intent `upstreams.list`.

| old | React | status |
|---|---|---|
| Title "Upstreams", subtitle "N targets (M healthy)", counting one per route entry | Title "Upstreams", subtitle saying each upstream appears once and is healthy only when every route's entry for it is. Caption "N upstreams" | Migrated: one row per distinct URL |
| Column "Target URL", mono | "URL", mono | Migrated |
| Column "Route", the route's path, one row per route | "Routes", the paths of every route using the upstream, each a link | Migrated |
| Column "Weight" | none | Dropped: a weight belongs to a route's entry for the upstream, and a distinct upstream has several. It is in the Targets table on each route's page |
| Column "Health", a green "Healthy" or red "Unhealthy" pill | "Health", an outline "Healthy" or destructive "Unhealthy" badge | Migrated |
| No circuit column | "Circuit", Closed (outline), Half-open (default), Open (destructive) | Migrated, new |
| No traffic columns | "Requests", "Errors", "Avg latency" (blank until the upstream has a request) | Migrated, new |
| Empty "No upstreams configured", a table row | "No upstreams. Add a route to give the gateway somewhere to send traffic." | Migrated |

`upstreams.list` also carries each upstream's active connection count. This page
doesn't show it. The Health page does, see Not classified below.

### Services

`renderServicesPage` and `renderServiceCards`, `render_pages.go:114` and `:531`.
Fed by `gw.Discovery().DiscoveredServices()`, which returned shared pointers that
discovery mutates later. The React page reads `services.list`, which copies, and
returns `discoveryEnabled`, `services` and `total`.

| old | contract field | status |
|---|---|---|
| Title "Services", subtitle "Discovered services via FARP" (empty) or "N discovered services" | `total` | Migrated |
| One card per service, in discovery order, in a three-column grid | `services`, sorted by name | Migrated |
| Card title, the service name | `name` | Migrated |
| Health pill, green "Healthy" or red "Unhealthy" | `healthy` | Migrated |
| Row "Version" | `version` | Migrated |
| Row "Address", `address:port` in mono | `address` and `port` | Migrated |
| Row "Routes", the route count | `routeCount` | Migrated |
| No protocols, no discovery time, no metadata | `protocols`, `discoveredAt`, `metadataKeys` | Migrated, new |
| Empty "No services discovered yet", the same whether discovery was off or found nothing | `discoveryEnabled` tells the two apart | Migrated: the page says "Discovery is switched off" separately |
| Refresh | command `discovery.refresh`, answers CONFLICT with `details.reason` `discoveryOff` when discovery is disabled | Migrated (the command also has a button on the Routes page) |

### Traffic

`renderTrafficPage` and `renderRouteStatsRows`, `render_pages.go:141` and `:556`.
The React page reads `traffic.stats`.

| old | contract field | status |
|---|---|---|
| Title "Traffic", subtitle "Real-time traffic metrics" (it was a snapshot at page load) | the page polls | Migrated |
| Stat "Total Requests", abbreviated | `totalRequests` | Migrated |
| Stat "Total Errors" | `totalErrors` | Migrated |
| Stat "Rate Limited" | `rateLimited` | Migrated |
| Stat "Circuit Breaks" (a running total of requests refused by an open breaker) | `circuitBreaks` | Migrated |
| Card "Per-Route Statistics", a table in map order (random) | `routes`, busiest first | Migrated |
| Column "Route", the route's path in mono | `routes[].path`, `routeId` | Migrated |
| Column "Requests" | `routes[].totalRequests` | Migrated |
| Column "Errors" | `routes[].totalErrors` | Migrated |
| Column "Avg Latency", always "0.0ms" | `routes[].avgLatencyMs`, now real, or `null` | Migrated: "Not measured" when null |
| No latency percentile, error rate, cache or retry figures | `p99LatencyMs`, `errorRate`, `latencySamples`, `cacheHits`, `cacheMisses`, `retriesMeasured` | Migrated, new. `retriesMeasured` is false today, so the page must say retries are not measured, not show 0 |
| Empty "No traffic data yet", a table row | `routes` empty | Migrated |

### Health

`renderHealthPage` and `renderHealthRows`, `render_pages.go:176` and `:575`. There
is no `health.list` query. The React page is served by `upstreams.list` (health,
counters, active connections and the routes using each upstream), one row per
upstream, and by `config.detail` for the health-check settings.

| old | contract field | status |
|---|---|---|
| Title "Health", subtitle "Upstream health status" | none | Migrated |
| Column "Target", the URL in mono | `upstreams[].url` | Migrated |
| Column "Route", the route path, one row per route entry | `upstreams[].routes` | Migrated: one row per upstream, with its routes listed |
| Column "Status", a green "Healthy" or red "Unhealthy" pill | `upstreams[].healthy` | Migrated |
| Column "Requests" | `upstreams[].totalRequests` | Migrated |
| Column "Errors" | `upstreams[].totalErrors` | Migrated |
| Empty "No targets configured" | `upstreams` empty | Migrated |
| Health history | none | Dropped: `health.History` is never constructed, so there was none and there is none. See Still open |

### Circuits

`renderCircuitsPage` and `renderCircuitRows`, `render_pages.go:200` and `:604`.
The React page reads `circuits.list` and runs the command `circuits.reset`.

| old | contract field | status |
|---|---|---|
| Title "Circuit Breakers", subtitle "Per-target circuit breaker states" | none | Migrated |
| Column "Target", the URL | `circuits[].url`, `targetId` | Migrated |
| Column "Route", the route path, one row per route entry | `circuits[].routes` | Migrated |
| Column "State", a pill: green for closed, yellow for half-open, red for open. Always "closed" in practice | `circuits[].state`, now the real breaker state | Migrated: badges are Closed (outline), Half-open (default), Open (destructive) |
| Column "Active Conns" | not in `circuits.list` | Migrated, on the Health page: `upstreams.list` carries `activeConns`, so the active connections show per upstream there |
| No breaker detail | `tracked`, `failureCount`, `lastFailure`, `lastStateChange` | Migrated, new. `tracked: false` means the target was never selected, so no breaker exists yet |
| No settings | `enabled`, `failureThreshold`, `resetTimeoutSeconds`, `halfOpenMax` | Migrated, new. With `enabled: false` the page must say circuit breaking is off, since "no open circuits" would read as good news |
| No actions | command `circuits.reset` with `targetId` | Migrated, new |
| Empty "No targets configured" | `circuits` empty | Migrated |

### API explorer

`renderAPIExplorerPage`, `render_pages.go:224`. The React page reads
`openapi.summary` and runs the command `openapi.refresh`.

| old | contract field | status |
|---|---|---|
| Title "API Explorer", subtitle "Aggregated OpenAPI specification from all upstream services" | none | Migrated |
| Empty state when there is no merged spec: "OpenAPI spec not available yet. Enable OpenAPI aggregation or wait for the first refresh." | `enabled` and `running` | Migrated: the page says which of the two it is, disabled in config or not started |
| Button "OpenAPI JSON", a link to `BasePath + SpecPath` in a new tab | `specPath` (already carries the base path) | Migrated |
| Button "Refresh Specs", a POST to `BasePath/api/openapi/refresh`, then a page reload | command `openapi.refresh` | Migrated: the refresh now outlives the request |
| Stat "Services", "Discovered", the number of service specs | `total` | Migrated |
| Stat "Healthy", "n/m", "Specs available" | `services[].healthy` | Migrated |
| Stat "Total Paths", "Across all services", the merged spec's `paths` count | `totalPaths` | Migrated |
| Stat "Last Refresh", "15:04:05" or "Never" | `lastRefresh` | Migrated |
| Card "Discovered Services", "OpenAPI specs from upstream services" | `services` | Migrated |
| Column "Service" | `services[].serviceName` | Migrated |
| Column "Version" | `services[].version` | Migrated |
| Column "Paths" | `services[].pathCount` | Migrated |
| Column "Status", a green "Healthy" or red "Error" pill, with the spec error text beside it | `services[].healthy`, `services[].error` | Migrated: the error text is rendered as text now, not as markup |
| Column "Spec", a "View Spec" link to `BasePath/api/openapi/services/<name>` | `services[].specUrl`, with credentials redacted | Migrated |
| Empty row "No upstream services discovered yet" | `services` empty | Migrated |
| Card "Swagger UI", "Interactive API documentation", an "Open in new tab" link and an 800 pixel iframe | none | Dropped: see Deliberately dropped |
| No fetch time per spec | `services[].fetchedAt` | Migrated, new |

### Config

`renderConfigPage`, `render_pages.go:368`. The React page reads `config.detail`, which
answers a list of sections, each with a title, an optional on or off switch, an
optional note and key and value settings. The old page printed 16 values in four
cards. The contract covers all of them and many more.

| old | contract section and setting | status |
|---|---|---|
| Title "Configuration", subtitle "Current gateway settings (read-only)" | none | Migrated |
| Card "General", row "Enabled" | `gateway`, switch | Migrated |
| Row "Base Path", "/" when empty | `gateway`, "Base path" | Migrated: shown as configured, so an empty one is empty |
| Row "Load Balancing", the strategy | `loadBalancing`, "Strategy" | Migrated |
| Card "Resilience", row "Circuit Breaker" | `circuitBreaker`, switch | Migrated |
| Row "Rate Limiting" | `rateLimiting`, switch | Migrated |
| Row "Retry", "true (max 3)" | `retry`, switch and "Max attempts" | Migrated: the section carries a note that nothing in the proxy calls the retry policy, so no request is retried |
| Row "Health Check" | `healthCheck`, switch | Migrated |
| Card "Security", row "Auth" | `auth`, switch | Migrated |
| Row "TLS" | `tls`, switch | Migrated: file paths show as "set" or "not set" |
| Row "IP Filter" | `ipFilter`, switch | Migrated: allow and deny lists show as counts |
| Row "CORS" | `cors`, switch | Migrated |
| Card "Features", row "Caching" | `caching`, switch | Migrated: the section carries a note that nothing writes to the cache |
| Row "Discovery" | `discovery`, switch | Migrated |
| Row "OpenAPI" | `openapi`, switch | Migrated |
| Row "Metrics" | `metrics`, switch | Migrated |
| Not on the old page | `timeouts`, `accessLog`, and the detail settings of every section above | Migrated, new |

### Widgets

`renderStatsWidgetHTML`, `renderHealthWidgetHTML` and `renderErrorsWidgetHTML`,
`render_widgets.go:12`, `:26`, `:54`, registered at `manifest.go:44`. All three
are dropped. The shell's overview replaces them and nothing else mounts a widget.

| old | React | status |
|---|---|---|
| Widget `bastion-stats`, "Gateway Stats", "Total requests, error rate, and average latency", size md, refresh 15 s, group Gateway | none | Dropped: see below for each figure |
| Its four cards: "Requests" (abbreviated), "Errors" (abbreviated), "Avg Latency" (always "0.0ms"), "Routes" | Overview "Requests" with the error count in its hint, "Latency", "Routes" | Migrated onto the overview. The description promised an error rate and the widget showed an error count |
| Widget `bastion-health`, "Route Health", "Healthy vs total upstream targets", size sm, refresh 30 s | none | Dropped |
| Its "Healthy Upstreams" label and "n / m" text | Overview "Upstreams", "N of M", "healthy" | Migrated onto the overview |
| Its bar, green at 80% and above, yellow from 50% to under 80%, red below 50%, and "N% of upstreams are healthy" | none | Dropped: the unhealthy badge on the upstreams page is the signal. It also divided by a per-route-entry total |
| Widget `bastion-errors`, "Error Rate", "Error percentage and open circuit count", size sm, refresh 15 s | none | Dropped |
| Its big percentage, green up to 1%, yellow above 1%, red above 5%, "Error Rate" | Overview "Error rate", "Not measured" until a request arrives | Migrated onto the overview, without the colour bands |
| Its "Circuit Breaks: N" and "Rate Limited: N" | Overview "Open circuits" (a count of breakers open now); "Circuit Breaks" and "Rate Limited" totals on the Traffic page | Migrated, in part: the description promised an open circuit count and the widget never showed one. The overview shows it for the first time. The two totals are on the Traffic page |

### Config settings panel

`renderConfigSettingsHTML`, `render_widgets.go:80`, registered at `manifest.go:73`
as `bastion-config`, "Gateway Configuration", "View current gateway settings",
group "Bastion", icon settings. Dropped. The shell has no per-plugin settings panel,
and `config.detail` covers it with the Config page.

| old | React | status |
|---|---|---|
| Row "Gateway Enabled" | Config page, `gateway` switch | Migrated |
| Row "Load Balancing" | Config page, `loadBalancing` | Migrated |
| Row "Circuit Breaker" | Config page, `circuitBreaker` | Migrated |
| Row "Rate Limiting" | Config page, `rateLimiting` | Migrated |
| Row "Auth" | Config page, `auth` | Migrated |
| Row "Caching" | Config page, `caching` | Migrated |
| Row "Discovery" | Config page, `discovery` | Migrated |
| Row "OpenAPI" | Config page, `openapi` | Migrated |
| The panel itself | none | Dropped |

### Actions

The old dashboard had exactly one: the explorer's "Refresh Specs" button (a plain
`fetch` POST followed by a reload). Everything else was read-only. The React
plugin has these, backed by the seven contract commands.

| command | where | status |
|---|---|---|
| `routes.create` | `/new-route` form | Migrated, new |
| `routes.update` | `/routes/:id/edit` form | Migrated, new |
| `routes.delete` | Delete on the route page, confirmed. Goes back to the routes list | Migrated, new |
| `routes.setEnabled` | Enable, and Disable (confirmed) on the route page. The answer carries `durable`, and the page says a config route's change lasts until restart, or that the gateway has no route store | Migrated, new |
| `discovery.refresh` | "Refresh discovery" on the routes page. Discovery off answers CONFLICT, and the page says so in words | Migrated, new |
| `openapi.refresh` | the API explorer's refresh | Migrated |
| `circuits.reset` | the circuits page | Migrated |

Commands that change a route invalidate `routes.list`, `routes.detail`,
`upstreams.list`, `overview.stats`, `traffic.stats` and `circuits.list`.
`routes.setEnabled` invalidates the first, second and fourth only. Every refusal
reaches the page with its code: a source refusal answers CONFLICT with
`details.reason` `source` and the route's source, a duplicate answers CONFLICT with
`details.reason` `duplicate` and the other route's id, and a validation error
answers BAD_REQUEST with `details.field`.

## Not classified

Two things on the old pages have no settled home, and we'd rather say so than
guess.

- "Active Conns" on the circuits page (`render_pages.go:604`). It was a real number
  (a count of in-flight requests per target, kept by `IncrConns` and `DecrConns`
  in the HTTP, gRPC and WebSocket proxies), not a dead one. `circuits.list` doesn't
  carry it. `upstreams.list` does (`activeConns`), and the Health page shows it per
  upstream. The Circuits page doesn't, so that column moved one page over.
- The health page's "Health" nav entry has no query of its own. It reads
  `upstreams.list` and `config.detail`. If you want a probe-now button or a
  history, neither exists in Go: there is no manual health check and
  `health.History` is never constructed.

## Still open

Not fixed by this migration, each worth its own follow-up. Most were never in the
old pages. We list them so nobody has to find them again.

Security:

- The admin REST API has no auth. `AdminAuthMiddleware` exists and is never wired.
- FARP push registration (`/_farp/v1/register`) has no auth. Whatever it stores is
  now rendered as text by the React pages, not as markup, but anyone who can reach
  the gateway can still add routes.
- FARP deregister is a no-op that answers "deregistered".
- The WebSocket hub's `CheckOrigin` returns true, and `Broadcast` blocks when its
  channel fills.
- Headers the editor does not show (including a redacted `Authorization`) follow a
  target repoint: an operator with write access can send them to a host they
  choose.

Behaviour the proxy doesn't deliver:

- The response cache never stores anything. `ResponseCache.Set` has no caller.
- Retries never happen. `RetryPolicy.ShouldRetry` has no caller.
- A 5xx answer never trips a breaker, only a transport error does, and the
  per-route `CircuitBreaker` override is never read (`GetWithConfig` has no
  callers).
- Timeout, retry, per-route breaker and cache overrides are stored and shown, but
  the proxy doesn't apply them.
- Rate-limit buckets are keyed by request path, so two routes on one path with
  different limits rebuild each other's bucket.
- Discovery marks unhealthy, or removes, config routes that share a `service_name`.

Races and state:

- `Target.Healthy` is a plain bool, written under the health monitor's lock and
  read without one, by the load balancer, by snapshots and by the admin views.
- REST `GET /routes/:id` and `GET /upstreams` still call the racy
  `Target.Snapshot()`. Only the contract handlers copy.
- The async route-change listener can re-register a removed target's health entry.
- Circuit and health state are persisted and never read back. `auditSink` is set
  and never written, so a dashboard write leaves one Info log line (intent,
  subject, operator) and no audit record.
- Enable and disable on a config-file route does not survive a restart:
  `loadManualRoutes` runs before `loadPersistedRoutes`, so the config wins.
- The route store saves edits of config routes. If the config route is later
  removed from the file, the edit comes back as a ghost route.
- `health.History` is never constructed, so there is no health history to show.

Concurrency between operators:

- Two operators editing one route: the last save wins.
- A stale editor page overwrites newer changes. The form sends every field it
  holds, so an edit saved from a page opened before another operator disabled the
  route enables it again. A version precondition would close this.

Build:

- `pnpm-lock.yaml` in `forge-dashboard` does not yet carry the `plugin-bastion`
  importer.

## What was not covered by tests

Plainly, so you know where to look first if something breaks.

- The store backends get no tests from this migration. Everything here ran against
  the in-memory route table, so a route persisted through grove, and read back after
  a restart, was not exercised by anything we wrote.
- The success paths of discovery refresh and OpenAPI refresh are covered only by
  fixtures. The Go handlers are tested for their refusals (CONFLICT with `discoveryOff`
  and `openapiOff`), and the React side runs against the fixture server, which models the
  fixed contract and not a gateway with real services behind it. No test ran a
  real discovery update or a real upstream spec fetch end to end.
- Nothing tests the gateway under load. The p99 figure's error bound is written
  down in the design, not measured against a real traffic mix.
- The plugin test checks that every nav entry has a route and that the host
  resolves the plugin. It does not click every link on every page to see where it
  lands.
- The last six pages have React tests against stubbed clients, and their
  contract handlers have Go tests. Nothing runs them together against a live
  gateway, and the fixture serves all nine queries.
- The old dashboard had no tests, so there is nothing to diff the new pages
  against except this file.
