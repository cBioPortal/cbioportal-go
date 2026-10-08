package config

// BackendProperties are the Java portal's server-side properties this server
// honours besides the frontend ones: the cache-eviction endpoint's switch and
// key, the SEO endpoints' flag and robots.txt policy, the /llms.txt file, and the session
// service's credentials and virtual-study publisher key (session.service.url
// is a frontend property as well).
var BackendProperties = []string{
	"cache.endpoint.enabled",
	"cache.endpoint.api-key",
	"sitemaps",
	"robots.disallow_paths",
	"robots.crawl_delay",
	"robots.disallow_user_agents",
	"llms_txt.location",
	"session.service.user",
	"session.service.password",
	"session.endpoint.publisher-api-key",
}
