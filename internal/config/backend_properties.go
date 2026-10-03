package config

// BackendProperties are the Java portal's server-side properties this server
// honours besides the frontend ones: the cache-eviction endpoint's switch and
// key, and the SEO endpoints' flag and robots.txt policy.
var BackendProperties = []string{
	"cache.endpoint.enabled",
	"cache.endpoint.api-key",
	"sitemaps",
	"robots.disallow_paths",
	"robots.crawl_delay",
	"robots.disallow_user_agents",
}
