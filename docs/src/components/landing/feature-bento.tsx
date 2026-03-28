"use client";

import { motion } from "framer-motion";
import { cn } from "@/lib/cn";
import { CodeBlock } from "./code-block";
import { SectionHeader } from "./section-header";

interface FeatureCard {
  title: string;
  description: string;
  icon: React.ReactNode;
  code: string;
  filename: string;
  colSpan?: number;
}

const features: FeatureCard[] = [
  {
    title: "Multi-Protocol Proxying",
    description:
      "Route HTTP, WebSocket, SSE, and gRPC traffic through a unified proxy engine. Each protocol gets its own optimized handler with connection pooling and graceful shutdown.",
    icon: (
      <svg
        className="size-5"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M22 12h-4l-3 9L9 3l-3 9H2" />
      </svg>
    ),
    code: `gateway.WithRoute(gateway.RouteConfig{
    Path:     "/api/users/*",
    Targets:  []gateway.TargetConfig{
        {URL: "http://user-svc:8080", Weight: 1},
    },
    Protocol:    gateway.ProtocolHTTP,
    StripPrefix: true,
    Enabled:     true,
})`,
    filename: "routes.go",
  },
  {
    title: "Service Discovery",
    description:
      "FARP-based schema-driven route generation from OpenAPI, AsyncAPI, and GraphQL descriptors. Services register once and the gateway auto-configures routes.",
    icon: (
      <svg
        className="size-5"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <circle cx="11" cy="11" r="8" />
        <path d="M21 21l-4.35-4.35" />
      </svg>
    ),
    code: `gateway.NewExtension(
    gateway.WithDiscoveryEnabled(true),
    gateway.WithDiscoveryConfig(gateway.DiscoveryConfig{
        WatchMode:  true,
        AutoPrefix: true,
        PrefixTemplate: "/{{.ServiceName}}",
    }),
)`,
    filename: "discovery.go",
  },
  {
    title: "Load Balancing",
    description:
      "Five strategies out of the box: round-robin, weighted round-robin, random, least-connections, and consistent hash. Per-route strategy configuration.",
    icon: (
      <svg
        className="size-5"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M12 3v18" />
        <path d="M3 12h18" />
        <path d="M6 6l4 6-4 6" />
        <path d="M18 6l-4 6 4 6" />
      </svg>
    ),
    code: `gateway.WithLoadBalancing(gateway.LoadBalancingConfig{
    Strategy: gateway.LBWeightedRoundRobin,
})
// Per-route override
route.LoadBalancing = &gateway.LoadBalancingConfig{
    Strategy: gateway.LBConsistentHash,
    HashKey:  "X-User-ID",
}`,
    filename: "balancing.go",
  },
  {
    title: "Circuit Breakers",
    description:
      "Per-target three-state circuit breakers (closed/open/half-open) with configurable failure thresholds, reset timeouts, and half-open probe limits.",
    icon: (
      <svg
        className="size-5"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
        <path d="M13 2l-1 9h4l-5 11" />
      </svg>
    ),
    code: `gateway.WithCircuitBreaker(gateway.CircuitBreakerConfig{
    Enabled:          true,
    FailureThreshold: 5,
    ResetTimeout:     30 * time.Second,
    HalfOpenRequests: 3,
})`,
    filename: "breaker.go",
  },
  {
    title: "Rate Limiting",
    description:
      "Token-bucket rate limiting at global, per-route, and per-client levels. Configurable burst allowance with automatic client identification.",
    icon: (
      <svg
        className="size-5"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M12 2a10 10 0 100 20 10 10 0 000-20z" />
        <path d="M12 6v6l4 2" />
        <path d="M16.24 7.76l1.42-1.42" />
      </svg>
    ),
    code: `gateway.WithRateLimiting(gateway.RateLimitConfig{
    Enabled:        true,
    RequestsPerSec: 1000,
    Burst:          100,
    PerClient:      true,
})`,
    filename: "ratelimit.go",
  },
  {
    title: "Traffic Splitting",
    description:
      "Canary releases, blue-green deployments, A/B testing, and shadow traffic mirroring. Route percentages of traffic to different upstream versions.",
    icon: (
      <svg
        className="size-5"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <line x1="6" y1="3" x2="6" y2="15" />
        <circle cx="18" cy="6" r="3" />
        <circle cx="6" cy="18" r="3" />
        <path d="M18 9a9 9 0 01-9 9" />
      </svg>
    ),
    code: `route.TrafficSplit = &gateway.TrafficSplitConfig{
    Strategy: gateway.SplitCanary,
    Rules: []gateway.SplitRule{
        {TargetURL: "http://v2:8080", Weight: 10}, // 10% canary
        {TargetURL: "http://v1:8080", Weight: 90}, // 90% stable
    },
}`,
    filename: "traffic.go",
    colSpan: 2,
  },
];

const containerVariants = {
  hidden: {},
  visible: {
    transition: {
      staggerChildren: 0.08,
    },
  },
};

const itemVariants = {
  hidden: { opacity: 0, y: 20 },
  visible: {
    opacity: 1,
    y: 0,
    transition: { duration: 0.5, ease: "easeOut" as const },
  },
};

export function FeatureBento() {
  return (
    <section className="relative w-full py-20 sm:py-28">
      <div className="container max-w-(--fd-layout-width) mx-auto px-4 sm:px-6">
        <SectionHeader
          badge="Features"
          title="Everything you need for API traffic management"
          description="Bastion handles the hard parts — routing, load balancing, circuit breaking, rate limiting, and traffic splitting — so you can focus on your services."
        />

        <motion.div
          variants={containerVariants}
          initial="hidden"
          whileInView="visible"
          viewport={{ once: true, margin: "-50px" }}
          className="mt-14 grid grid-cols-1 md:grid-cols-2 gap-4"
        >
          {features.map((feature) => (
            <motion.div
              key={feature.title}
              variants={itemVariants}
              className={cn(
                "group relative rounded-xl border border-fd-border bg-fd-card/50 backdrop-blur-sm p-6 hover:border-blue-500/20 hover:bg-fd-card/80 transition-all duration-300",
                feature.colSpan === 2 && "md:col-span-2",
              )}
            >
              {/* Header */}
              <div className="flex items-start gap-3 mb-4">
                <div className="flex items-center justify-center size-9 rounded-lg bg-blue-500/10 text-blue-600 dark:text-blue-400 shrink-0">
                  {feature.icon}
                </div>
                <div>
                  <h3 className="text-sm font-semibold text-fd-foreground">
                    {feature.title}
                  </h3>
                  <p className="text-xs text-fd-muted-foreground mt-1 leading-relaxed">
                    {feature.description}
                  </p>
                </div>
              </div>

              {/* Code snippet */}
              <CodeBlock
                code={feature.code}
                filename={feature.filename}
                showLineNumbers={false}
                className="text-xs"
              />
            </motion.div>
          ))}
        </motion.div>
      </div>
    </section>
  );
}
