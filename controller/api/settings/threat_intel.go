package settings

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// api_collector_threatintel — the singleton threat-intel feed document
// the collector polls (tifeed package). Same versioned-singleton
// pattern as api_collector_config: the collector reacts only to a
// `version` change, so every write here MUST bump it.
const (
	threatIntelCollection = "api_collector_threatintel"
	threatIntelID         = "default"

	// maxEntriesPerFeed caps a single feed's entry count.
	maxEntriesPerFeed = 250_000

	// maxTotalEntries caps the entry count across ALL feeds in the
	// document. A MongoDB document is hard-limited to 16 MB; the whole
	// feeds[] array is stored in one doc, so a per-feed cap alone is
	// not enough — three full 250k feeds would blow past 16 MB and the
	// UpdateOne would fail with an opaque BSONObjectTooLarge. ~25 bytes
	// per CIDR string + BSON overhead → 600k entries ≈ 12 MB, a safe
	// ceiling that still leaves head-room.
	maxTotalEntries = 600_000

	// maxUploadSkipSamples bounds how many bad lines we echo back so
	// the response stays small even on a wholly malformed file.
	maxUploadSkipSamples = 20
)

// tiFeed mirrors the collector's tifeed.Feed BSON shape. Enabled is a
// pointer — nil means enabled (older tooling without the key stays
// active); the UI must send an explicit value when toggling.
type tiFeed struct {
	Name    string   `bson:"name" json:"name"`
	Enabled *bool    `bson:"enabled,omitempty" json:"enabled,omitempty"`
	Entries []string `bson:"entries" json:"entries"`
}

// tiDoc mirrors the collector's tifeed.Doc.
type tiDoc struct {
	ID        string    `bson:"_id" json:"-"`
	Version   int64     `bson:"version" json:"version"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
	UpdatedBy string    `bson:"updated_by,omitempty" json:"updated_by,omitempty"`
	Feeds     []tiFeed  `bson:"feeds" json:"feeds"`
}

// tiFeedSummary is the per-feed shape returned by the list endpoint —
// entry_count instead of the (potentially huge) entries array. BSON
// tags match the $project output of GetThreatIntel's aggregation.
type tiFeedSummary struct {
	Name       string `bson:"name" json:"name"`
	Enabled    *bool  `bson:"enabled" json:"enabled,omitempty"`
	EntryCount int    `bson:"entry_count" json:"entry_count"`
}

// tiSummaryDoc is the projected document GetThreatIntel decodes.
type tiSummaryDoc struct {
	Version   int64           `bson:"version"`
	UpdatedAt time.Time       `bson:"updated_at"`
	UpdatedBy string          `bson:"updated_by"`
	Feeds     []tiFeedSummary `bson:"feeds"`
}

// GetThreatIntel handles GET /api/v3/setting/threat-intel.
//
// Returns the feed document with per-feed summaries — entry COUNTS,
// not the entries arrays (a busy fleet's feeds can hold hundreds of
// thousands of IPs). A `$project` with `$size` computes the counts
// server-side, so the entries never cross the wire; the full list of
// one feed is available via GET .../threat-intel/:name.
// Admin/Owner only — InitSettingMiddleware gates the /setting group.
func (handler *AppHandler) GetThreatIntel(c *gin.Context) {
	ctx := c.Request.Context()

	pipeline := []bson.M{
		{"$match": bson.M{"_id": threatIntelID}},
		{"$project": bson.M{
			"version":    1,
			"updated_at": 1,
			"updated_by": 1,
			"feeds": bson.M{"$map": bson.M{
				"input": bson.M{"$ifNull": bson.A{"$feeds", bson.A{}}},
				"as":    "f",
				"in": bson.M{
					"name":        "$$f.name",
					"enabled":     "$$f.enabled",
					"entry_count": bson.M{"$size": bson.M{"$ifNull": bson.A{"$$f.entries", bson.A{}}}},
				},
			}},
		}},
	}
	cur, err := handler.Context.Client.Collection(threatIntelCollection).Aggregate(ctx, pipeline)
	if err != nil {
		handler.Logger.Errorf("threat-intel read failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to read threat-intel feeds"})
		return
	}
	defer cur.Close(ctx)
	var docs []tiSummaryDoc
	if err := cur.All(ctx, &docs); err != nil {
		handler.Logger.Errorf("threat-intel decode failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to read threat-intel feeds"})
		return
	}
	if len(docs) == 0 {
		// Collector hasn't created the singleton yet.
		c.JSON(http.StatusOK, gin.H{"version": 0, "feeds": []tiFeedSummary{}})
		return
	}

	doc := docs[0]
	feeds := doc.Feeds
	if feeds == nil {
		feeds = []tiFeedSummary{}
	}
	c.JSON(http.StatusOK, gin.H{
		"version":    doc.Version,
		"updated_at": doc.UpdatedAt,
		"updated_by": doc.UpdatedBy,
		"feeds":      feeds,
	})
}

// GetThreatIntelFeed handles GET /api/v3/setting/threat-intel/:name.
// Returns a single feed with its full entries list.
func (handler *AppHandler) GetThreatIntelFeed(c *gin.Context) {
	ctx := c.Request.Context()
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "feed name is required"})
		return
	}

	doc, err := handler.loadThreatIntelDoc(ctx)
	if err != nil {
		handler.Logger.Errorf("threat-intel read failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to read threat-intel feeds"})
		return
	}
	if doc != nil {
		for _, f := range doc.Feeds {
			if f.Name == name {
				c.JSON(http.StatusOK, gin.H{"feed": f})
				return
			}
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"message": fmt.Sprintf("feed %q not found", name)})
}

// UpdateThreatIntel handles PUT /api/v3/setting/threat-intel.
//
// Whole-replace of the `feeds[]` array — the UI sends the complete
// feed set (enable/disable toggles, manual entry edits). Admin/Owner
// only. Bumps `version` so the collector hot-reloads.
func (handler *AppHandler) UpdateThreatIntel(c *gin.Context) {
	if !handler.canWriteCollectorConfig(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "only Admin or Owner can change threat-intel feeds"})
		return
	}

	var body struct {
		Feeds []tiFeed `json:"feeds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body: " + err.Error()})
		return
	}
	if err := validateThreatIntelFeeds(body.Feeds); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	updated, err := handler.writeThreatIntelFeeds(c, body.Feeds)
	if err != nil {
		handler.Logger.Errorf("threat-intel update failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to update threat-intel feeds"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "threat-intel feeds updated", "version": updated.Version})
}

// UploadThreatIntelFeed handles POST /api/v3/setting/threat-intel/upload.
//
// Multipart: `name` form field + `file`. The file is parsed one
// IP/CIDR per line; blank lines and `#` comments are skipped, invalid
// lines are counted (not fatal — the feed is never rejected, matching
// the collector's tolerant parse). The named feed is merged into
// feeds[] (entries replaced if it exists, appended otherwise).
// Admin/Owner only.
func (handler *AppHandler) UploadThreatIntelFeed(c *gin.Context) {
	if !handler.canWriteCollectorConfig(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "only Admin or Owner can change threat-intel feeds"})
		return
	}

	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "feed 'name' form field is required"})
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "feed 'file' upload is required"})
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not open uploaded file"})
		return
	}
	defer f.Close()

	entries := make([]string, 0, 1024)
	skipped := 0
	skipSamples := make([]string, 0, maxUploadSkipSamples)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !validIPOrCIDR(line) {
			skipped++
			if len(skipSamples) < maxUploadSkipSamples {
				skipSamples = append(skipSamples, line)
			}
			continue
		}
		entries = append(entries, line)
		if len(entries) > maxEntriesPerFeed {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": fmt.Sprintf("feed exceeds %d entries; split it or trim the file", maxEntriesPerFeed),
			})
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "failed to read uploaded file: " + err.Error()})
		return
	}

	// Merge into the current feed set: replace the named feed's
	// entries if present, otherwise append a new feed. enabled is
	// preserved from the existing feed (nil = enabled for new ones).
	doc, err := handler.loadThreatIntelDoc(c.Request.Context())
	if err != nil {
		handler.Logger.Errorf("threat-intel read failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to read threat-intel feeds"})
		return
	}
	feeds := []tiFeed{}
	if doc != nil {
		feeds = doc.Feeds
	}
	merged := false
	for i := range feeds {
		if feeds[i].Name == name {
			feeds[i].Entries = entries
			merged = true
			break
		}
	}
	if !merged {
		feeds = append(feeds, tiFeed{Name: name, Entries: entries})
	}

	// Guard the merged set against the 16 MB document ceiling — a new
	// feed (or a grown one) could push the total past maxTotalEntries.
	if err := validateThreatIntelFeeds(feeds); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	updated, err := handler.writeThreatIntelFeeds(c, feeds)
	if err != nil {
		handler.Logger.Errorf("threat-intel upload write failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to save threat-intel feed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":         "threat-intel feed uploaded",
		"feed":            name,
		"added":           len(entries),
		"skipped":         skipped,
		"skipped_samples": skipSamples,
		"version":         updated.Version,
	})
}

// DeleteThreatIntelFeed handles DELETE /api/v3/setting/threat-intel/:name.
// Drops one feed from feeds[]. Admin/Owner only.
func (handler *AppHandler) DeleteThreatIntelFeed(c *gin.Context) {
	if !handler.canWriteCollectorConfig(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "only Admin or Owner can change threat-intel feeds"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "feed name is required"})
		return
	}

	doc, err := handler.loadThreatIntelDoc(c.Request.Context())
	if err != nil {
		handler.Logger.Errorf("threat-intel read failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to read threat-intel feeds"})
		return
	}
	if doc == nil {
		c.JSON(http.StatusNotFound, gin.H{"message": fmt.Sprintf("feed %q not found", name)})
		return
	}
	kept := make([]tiFeed, 0, len(doc.Feeds))
	found := false
	for _, f := range doc.Feeds {
		if f.Name == name {
			found = true
			continue
		}
		kept = append(kept, f)
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"message": fmt.Sprintf("feed %q not found", name)})
		return
	}

	updated, err := handler.writeThreatIntelFeeds(c, kept)
	if err != nil {
		handler.Logger.Errorf("threat-intel delete write failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to delete threat-intel feed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("feed %q deleted", name), "version": updated.Version})
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

// loadThreatIntelDoc reads the singleton feed doc. Returns (nil, nil)
// when the collector has not created it yet — callers treat that as an
// empty feed set, not an error.
func (handler *AppHandler) loadThreatIntelDoc(ctx context.Context) (*tiDoc, error) {
	var doc tiDoc
	err := handler.Context.Client.Collection(threatIntelCollection).
		FindOne(ctx, bson.M{"_id": threatIntelID}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// writeThreatIntelFeeds persists the feed set: $set feeds + audit
// stamps, $inc version, upsert. The version bump is what the
// collector watches — without it a feed change would sit in Mongo
// unapplied.
func (handler *AppHandler) writeThreatIntelFeeds(c *gin.Context, feeds []tiFeed) (*tiDoc, error) {
	updatedBy := currentUsername(c)
	if updatedBy == "" {
		updatedBy = "unknown"
	}
	if feeds == nil {
		feeds = []tiFeed{}
	}

	ctx := c.Request.Context()
	coll := handler.Context.Client.Collection(threatIntelCollection)
	update := bson.M{
		"$set": bson.M{
			"feeds":      feeds,
			"updated_at": time.Now().UTC(),
			"updated_by": updatedBy,
		},
		"$inc": bson.M{"version": int64(1)},
	}
	if _, err := coll.UpdateOne(ctx, bson.M{"_id": threatIntelID}, update,
		options.Update().SetUpsert(true)); err != nil {
		return nil, err
	}
	var updated tiDoc
	if err := coll.FindOne(ctx, bson.M{"_id": threatIntelID}).Decode(&updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// validateThreatIntelFeeds checks the full feed set before it is
// persisted: every feed needs a non-empty unique name, no feed may
// exceed the per-feed cap, and the entries across ALL feeds must stay
// under maxTotalEntries — otherwise the single-document write would
// hit MongoDB's 16 MB ceiling and fail opaquely. Individual entry
// strings are NOT validated here (the collector parses tolerantly and
// the upload path already filters bad lines).
func validateThreatIntelFeeds(feeds []tiFeed) error {
	seen := make(map[string]struct{}, len(feeds))
	total := 0
	for i, f := range feeds {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			return fmt.Errorf("feeds[%d].name is required", i)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("duplicate feed name %q", name)
		}
		seen[name] = struct{}{}
		if len(f.Entries) > maxEntriesPerFeed {
			return fmt.Errorf("feed %q exceeds the %d-entry per-feed limit", name, maxEntriesPerFeed)
		}
		total += len(f.Entries)
	}
	if total > maxTotalEntries {
		return fmt.Errorf("total entries across all feeds (%d) exceed the %d limit — trim or remove a feed", total, maxTotalEntries)
	}
	return nil
}

// validIPOrCIDR reports whether s is a bare IP (v4/v6) or a CIDR
// block — the two forms the collector's matcher accepts.
func validIPOrCIDR(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if _, _, err := net.ParseCIDR(s); err == nil {
		return true
	}
	return false
}
