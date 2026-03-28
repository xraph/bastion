package dashboard

import (
	"github.com/xraph/forge/extensions/dashboard/contributor"
)

func boolPtr(b bool) *bool { return &b }

// NewManifest returns the dashboard manifest for Bastion.
func NewManifest() *contributor.Manifest {
	return &contributor.Manifest{
		Name:        "bastion",
		DisplayName: "Bastion",
		Icon:        "shield",
		Version:     "1.0.0",
		Layout:      "extension",
		ShowSidebar: boolPtr(true),
		TopbarConfig: &contributor.TopbarConfig{
			Title:       "Bastion Gateway",
			LogoIcon:    "shield",
			AccentColor: "#6366f1",
			ShowSearch:  false,
		},
		Nav:      baseNav(),
		Widgets:  baseWidgets(),
		Settings: baseSettings(),
	}
}

func baseNav() []contributor.NavItem {
	return []contributor.NavItem{
		{Label: "Overview", Path: "/", Icon: "layout-dashboard", Group: "Gateway", Priority: 0},
		{Label: "Routes", Path: "/routes", Icon: "route", Group: "Routing", Priority: 10},
		{Label: "Upstreams", Path: "/upstreams", Icon: "server", Group: "Routing", Priority: 11},
		{Label: "Services", Path: "/services", Icon: "network", Group: "Routing", Priority: 12},
		{Label: "Traffic", Path: "/traffic", Icon: "activity", Group: "Traffic", Priority: 20},
		{Label: "Health", Path: "/health", Icon: "heart-pulse", Group: "Resilience", Priority: 30},
		{Label: "Circuits", Path: "/circuits", Icon: "toggle-left", Group: "Resilience", Priority: 31},
		{Label: "API Explorer", Path: "/api-explorer", Icon: "globe", Group: "API", Priority: 40},
		{Label: "Config", Path: "/config", Icon: "settings", Group: "Settings", Priority: 50},
	}
}

func baseWidgets() []contributor.WidgetDescriptor {
	return []contributor.WidgetDescriptor{
		{
			ID:          "bastion-stats",
			Title:       "Gateway Stats",
			Description: "Total requests, error rate, and average latency",
			Size:        "md",
			RefreshSec:  15,
			Group:       "Gateway",
		},
		{
			ID:          "bastion-health",
			Title:       "Route Health",
			Description: "Healthy vs total upstream targets",
			Size:        "sm",
			RefreshSec:  30,
			Group:       "Gateway",
		},
		{
			ID:          "bastion-errors",
			Title:       "Error Rate",
			Description: "Error percentage and open circuit count",
			Size:        "sm",
			RefreshSec:  15,
			Group:       "Gateway",
		},
	}
}

func baseSettings() []contributor.SettingsDescriptor {
	return []contributor.SettingsDescriptor{
		{
			ID:          "bastion-config",
			Title:       "Gateway Configuration",
			Description: "View current gateway settings",
			Group:       "Bastion",
			Icon:        "settings",
		},
	}
}
