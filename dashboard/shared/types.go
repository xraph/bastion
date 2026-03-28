package shared

// GatewayOverview holds summary data for the overview page.
type GatewayOverview struct {
	TotalRoutes      int     `json:"totalRoutes"`
	HealthyUpstreams int     `json:"healthyUpstreams"`
	TotalUpstreams   int     `json:"totalUpstreams"`
	TotalRequests    int64   `json:"totalRequests"`
	ErrorRate        float64 `json:"errorRate"`
	AvgLatencyMs     float64 `json:"avgLatencyMs"`
	CacheHitRate     float64 `json:"cacheHitRate"`
	OpenCircuits     int     `json:"openCircuits"`
}

// PaginationMeta holds pagination metadata.
type PaginationMeta struct {
	Total       int64
	Limit       int
	Offset      int
	CurrentPage int
	TotalPages  int
}

// NewPaginationMeta creates pagination metadata.
func NewPaginationMeta(total int64, limit, offset int) PaginationMeta {
	totalPages := 1
	if limit > 0 && total > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}

	currentPage := 1
	if limit > 0 {
		currentPage = offset/limit + 1
	}

	return PaginationMeta{
		Total:       total,
		Limit:       limit,
		Offset:      offset,
		CurrentPage: currentPage,
		TotalPages:  totalPages,
	}
}
