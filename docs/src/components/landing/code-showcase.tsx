"use client";

import { motion } from "framer-motion";
import { CodeBlock } from "./code-block";
import { SectionHeader } from "./section-header";

const routesCode = `package main

import (
  "github.com/xraph/forge"
  "github.com/xraph/bastion"
)

func main() {
  app := forge.NewApp(forge.AppConfig{
    Name: "api-gateway",
    Extensions: []forge.Extension{
      bastion.NewExtension(
        bastion.WithRoute(bastion.RouteConfig{
          Path:    "/users/*",
          Targets: []bastion.TargetConfig{
            {URL: "http://user-svc:8080", Weight: 1},
          },
          StripPrefix: true,
          Protocol:    bastion.ProtocolHTTP,
          Enabled:     true,
        }),
      ),
    },
  })
  app.Run()
}`;

const discoveryCode = `package main

import (
  "github.com/xraph/forge"
  "github.com/xraph/bastion"
  "github.com/xraph/forge/extensions/discovery"
)

func main() {
  app := forge.NewApp(forge.AppConfig{
    Name: "api-gateway",
    Extensions: []forge.Extension{
      discovery.NewExtension(
        discovery.WithEnabled(true),
        discovery.WithBackend("consul"),
      ),
      bastion.NewExtension(
        bastion.WithDiscoveryEnabled(true),
        bastion.WithDashboardEnabled(true),
      ),
    },
  })
  app.Run()
}`;

export function CodeShowcase() {
  return (
    <section className="relative w-full py-20 sm:py-28">
      <div className="container max-w-(--fd-layout-width) mx-auto px-4 sm:px-6">
        <SectionHeader
          badge="Developer Experience"
          title="Simple API. Powerful gateway."
          description="Configure routes statically or let FARP auto-discover services. Bastion handles proxying, balancing, and resilience."
        />

        <div className="mt-14 grid grid-cols-1 lg:grid-cols-2 gap-6">
          {/* Static Routes side */}
          <motion.div
            initial={{ opacity: 0, x: -20 }}
            whileInView={{ opacity: 1, x: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.5, delay: 0.1 }}
          >
            <div className="mb-3 flex items-center gap-2">
              <div className="size-2 rounded-full bg-blue-500" />
              <span className="text-xs font-medium text-fd-muted-foreground uppercase tracking-wider">
                Static Routes
              </span>
            </div>
            <CodeBlock code={routesCode} filename="main.go" />
          </motion.div>

          {/* Auto-Discovery side */}
          <motion.div
            initial={{ opacity: 0, x: 20 }}
            whileInView={{ opacity: 1, x: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.5, delay: 0.2 }}
          >
            <div className="mb-3 flex items-center gap-2">
              <div className="size-2 rounded-full bg-indigo-500" />
              <span className="text-xs font-medium text-fd-muted-foreground uppercase tracking-wider">
                Auto-Discovery
              </span>
            </div>
            <CodeBlock code={discoveryCode} filename="discovery.go" />
          </motion.div>
        </div>
      </div>
    </section>
  );
}
