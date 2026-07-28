package registry

import (
	"container/list"
	"sync"

	"github.com/home-operations/ocharted/internal/oci"
)

// artifactCache is a byte-bounded LRU of derived artifacts, indexed three
// ways: by (repo, chart, version) for tag pulls, and by manifest and blob
// digest for the by-digest requests that follow. It is purely an
// optimization: every entry can be re-derived from upstream, so eviction and
// restarts never affect correctness — only how much work a cold request does.
type artifactCache struct {
	mu   sync.Mutex
	max  int64
	size int64
	ll   *list.List
	byID map[string]*list.Element
}

type cacheEntry struct {
	art   *oci.Artifact
	bytes int64
	// ids are every key this entry is findable under (version, manifest
	// digest, blob digests), kept for removal on eviction.
	ids []string
}

func newArtifactCache(maxBytes int64) *artifactCache {
	return &artifactCache{
		max:  maxBytes,
		ll:   list.New(),
		byID: map[string]*list.Element{},
	}
}

// Keys are namespaced by repo+chart so a cache hit never crosses a name
// boundary: the same content requested under a different (possibly
// allowlist-rejected) name must go through resolution and its checks.
func versionKey(repo, chart, version string) string { return repo + "/" + chart + "@v:" + version }
func digestKey(repo, chart, digest string) string   { return repo + "/" + chart + "@d:" + digest }

func (c *artifactCache) get(id string) (*oci.Artifact, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byID[id]
	if !ok {
		cacheEvents.WithLabelValues("miss").Inc()
		return nil, false
	}
	c.ll.MoveToFront(el)
	cacheEvents.WithLabelValues("hit").Inc()
	return el.Value.(*cacheEntry).art, true
}

func (c *artifactCache) add(repo, chart string, art *oci.Artifact) {
	entry := &cacheEntry{
		art:   art,
		bytes: int64(len(art.Chart) + len(art.Config) + len(art.Manifest) + len(art.Prov)),
		ids: []string{
			versionKey(repo, chart, art.Version),
			digestKey(repo, chart, art.ManifestDigest),
			digestKey(repo, chart, art.ConfigDigest),
			digestKey(repo, chart, art.ChartDigest),
		},
	}
	if art.Prov != nil {
		entry.ids = append(entry.ids, digestKey(repo, chart, art.ProvDigest))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byID[entry.ids[0]]; ok {
		// Already cached (a singleflight race); refresh recency and keep the
		// existing entry so the digest indexes stay consistent.
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(entry)
	for _, id := range entry.ids {
		c.byID[id] = el
	}
	c.size += entry.bytes

	for c.size > c.max && c.ll.Len() > 1 {
		c.evictOldest()
	}
}

func (c *artifactCache) evictOldest() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	entry := el.Value.(*cacheEntry)
	c.ll.Remove(el)
	for _, id := range entry.ids {
		delete(c.byID, id)
	}
	c.size -= entry.bytes
	cacheEvents.WithLabelValues("evict").Inc()
}
