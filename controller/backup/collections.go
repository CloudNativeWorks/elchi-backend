package backup

// CollectionMetadata defines the restore order and properties of each collection
type CollectionMetadata struct {
	Name     string
	Order    int    // Restore order (1-20)
	Category string // "settings", "xds", "templates", "services"
}

// RestoreOrder defines the dependency-aware restore sequence.
//
// NOTE: the elchi-collector-owned collections (api_inventory,
// api_collector_config, api_collector_threatintel, api_collector_baselines)
// are intentionally NOT listed here. They hold collector-internal state and
// observed-traffic data — not project configuration — so they are excluded
// from project backup/restore by design. Do not add them.
var RestoreOrder = []CollectionMetadata{
	// Phase 1: Settings (1-4)
	{Name: "projects", Order: 1, Category: "settings"},
	{Name: "users", Order: 2, Category: "settings"},
	{Name: "groups", Order: 3, Category: "settings"},
	{Name: "settings", Order: 4, Category: "settings"},

	// Phase 2: XDS Resources - Bottom-up (5-14)
	{Name: "secrets", Order: 5, Category: "xds"},
	{Name: "endpoints", Order: 6, Category: "xds"},
	{Name: "extensions", Order: 7, Category: "xds"},
	{Name: "clusters", Order: 8, Category: "xds"},
	{Name: "virtual_hosts", Order: 9, Category: "xds"},
	{Name: "routes", Order: 10, Category: "xds"},
	{Name: "filters", Order: 11, Category: "xds"},
	{Name: "listeners", Order: 12, Category: "xds"},
	{Name: "bootstrap", Order: 13, Category: "xds"},
	{Name: "tls", Order: 14, Category: "xds"},

	// Phase 3: Templates (15-17)
	{Name: "resource_templates", Order: 15, Category: "templates"},
	{Name: "snippets", Order: 16, Category: "templates"},
	{Name: "scenarios", Order: 17, Category: "templates"},

	// Phase 4: Services & Clients (18-21)
	{Name: "clients", Order: 18, Category: "services"},
	{Name: "services", Order: 19, Category: "services"},
	{Name: "admin_ports", Order: 20, Category: "services"},
	{Name: "waf", Order: 21, Category: "services"},

	// Phase 5: ACME Certificate Management (22-25)
	{Name: "acme_accounts", Order: 22, Category: "acme"},
	{Name: "acme_dns_credentials", Order: 23, Category: "acme"},
	{Name: "acme_temp_keys", Order: 24, Category: "acme"},
	{Name: "acme_certificates", Order: 25, Category: "acme"},
}
