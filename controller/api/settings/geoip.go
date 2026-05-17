package settings

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oschwald/maxminddb-golang"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/gridfs"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GeoIP MMDB databases are stored in the `geoip` GridFS bucket, one
// file per kind. The collector's geoipsync package polls this bucket
// and pulls the newest file per `metadata.kind` — see the collector
// contract: metadata.kind is MANDATORY (a file without it is
// invisible to the collector), metadata.sha256 is verified after
// download.
const (
	geoipBucketName = "geoip"
	geoipKindCity   = "city"
	geoipKindASN    = "asn"

	// maxMMDBSize caps an uploaded / downloaded database. A paid
	// MaxMind GeoIP2-City file is ~120 MB; 256 MB leaves head-room
	// without letting a bad upload exhaust memory (the file is held
	// in RAM for the MMDB validation + sha256 + GridFS write).
	maxMMDBSize = 256 << 20 // 256 MiB

	// dbipDownloadTimeout bounds the db-ip Lite fetch — an ~80 MB
	// gzip over a slow link still needs a generous ceiling.
	dbipDownloadTimeout = 5 * time.Minute
)

// geoipFileMeta is the subset of a `geoip.files` GridFS document the
// settings layer reads back.
type geoipFileMeta struct {
	ID         primitive.ObjectID `bson:"_id"`
	Length     int64              `bson:"length"`
	UploadDate time.Time          `bson:"uploadDate"`
	Metadata   struct {
		Kind       string    `bson:"kind"`
		SHA256     string    `bson:"sha256"`
		Source     string    `bson:"source"`
		UploadedBy string    `bson:"uploaded_by"`
		UploadedAt time.Time `bson:"uploaded_at"`
	} `bson:"metadata"`
}

// GetGeoIP handles GET /api/v3/setting/geoip.
//
// Returns the currently-active (newest) MMDB per kind — the same file
// the collector would pull. Admin/Owner only — InitSettingMiddleware
// gates the whole /setting group.
func (handler *AppHandler) GetGeoIP(c *gin.Context) {
	ctx := c.Request.Context()
	bucket, err := handler.geoipBucket()
	if err != nil {
		handler.Logger.Errorf("geoip bucket init failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to access geoip storage"})
		return
	}
	files := bucket.GetFilesCollection()

	databases := make([]gin.H, 0, 2)
	for _, kind := range []string{geoipKindCity, geoipKindASN} {
		meta, err := latestGeoIPFile(ctx, files, kind)
		if err != nil {
			handler.Logger.Errorf("geoip %s lookup failed: %v", kind, err)
			c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to read geoip databases"})
			return
		}
		entry := gin.H{"kind": kind, "present": false}
		if meta != nil {
			entry = gin.H{
				"kind":        kind,
				"present":     true,
				"file_id":     meta.ID.Hex(),
				"size":        meta.Length,
				"sha256":      meta.Metadata.SHA256,
				"source":      meta.Metadata.Source,
				"uploaded_by": meta.Metadata.UploadedBy,
				"upload_date": meta.UploadDate,
			}
		}
		databases = append(databases, entry)
	}
	c.JSON(http.StatusOK, gin.H{"databases": databases})
}

// UploadGeoIP handles POST /api/v3/setting/geoip/upload.
//
// Multipart: `kind` (city|asn) + `file` (an .mmdb). The file is
// validated as a real MaxMind DB AND its database_type is matched
// against the requested kind — so an operator can't publish an ASN
// database under `city`. Admin/Owner only.
func (handler *AppHandler) UploadGeoIP(c *gin.Context) {
	if !handler.canWriteCollectorConfig(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "only Admin or Owner can publish geoip databases"})
		return
	}
	kind, ok := normaliseGeoIPKind(c.PostForm("kind"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "kind must be 'city' or 'asn'"})
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "'file' upload is required"})
		return
	}
	if fileHeader.Size > maxMMDBSize {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": fmt.Sprintf("file exceeds the %d MiB limit", maxMMDBSize>>20),
		})
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not open uploaded file"})
		return
	}
	defer f.Close()
	data, err := readCapped(f, maxMMDBSize)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	if err := validateMMDB(data, kind); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	res, err := handler.publishMMDB(c.Request.Context(), kind, data, "upload", currentUsername(c))
	if err != nil {
		handler.Logger.Errorf("geoip publish failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to publish geoip database"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "geoip database published",
		"kind":    kind,
		"file_id": res.fileID,
		"size":    res.size,
		"sha256":  res.sha256,
	})
}

// DownloadGeoIP handles POST /api/v3/setting/geoip/download.
//
// Fetches the free db-ip Lite database for the requested kind and
// publishes it to GridFS — same destination as an operator upload.
// Body: { "kind": "city" | "asn" }. Admin/Owner only.
func (handler *AppHandler) DownloadGeoIP(c *gin.Context) {
	if !handler.canWriteCollectorConfig(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "only Admin or Owner can publish geoip databases"})
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body: " + err.Error()})
		return
	}
	kind, ok := normaliseGeoIPKind(body.Kind)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "kind must be 'city' or 'asn'"})
		return
	}

	// db-ip download can take minutes; give it its own deadline
	// rather than riding the request context (the client may give up
	// first, but the publish should still complete).
	dlCtx, cancel := context.WithTimeout(context.Background(), dbipDownloadTimeout)
	defer cancel()
	data, srcURL, err := downloadDBIPLite(dlCtx, kind)
	if err != nil {
		handler.Logger.Errorf("db-ip download failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"message": "failed to download db-ip Lite database: " + err.Error()})
		return
	}
	if err := validateMMDB(data, kind); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "downloaded file is not a valid " + kind + " MMDB: " + err.Error()})
		return
	}

	res, err := handler.publishMMDB(context.Background(), kind, data, "dbip-lite", currentUsername(c))
	if err != nil {
		handler.Logger.Errorf("geoip publish failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to publish geoip database"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":    "db-ip Lite database published",
		"kind":       kind,
		"source_url": srcURL,
		"file_id":    res.fileID,
		"size":       res.size,
		"sha256":     res.sha256,
	})
}

// DeleteGeoIP handles DELETE /api/v3/setting/geoip/:kind.
//
// Removes ALL GridFS files for the kind — the collector then sees no
// file for it and (in lenient mode) runs with that database disabled.
// Admin/Owner only.
func (handler *AppHandler) DeleteGeoIP(c *gin.Context) {
	if !handler.canWriteCollectorConfig(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "only Admin or Owner can delete geoip databases"})
		return
	}
	kind, ok := normaliseGeoIPKind(c.Param("kind"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "kind must be 'city' or 'asn'"})
		return
	}

	ctx := c.Request.Context()
	bucket, err := handler.geoipBucket()
	if err != nil {
		handler.Logger.Errorf("geoip bucket init failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to access geoip storage"})
		return
	}
	deleted, err := deleteAllGeoIPFiles(ctx, bucket, kind)
	if err != nil {
		handler.Logger.Errorf("geoip delete failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to delete geoip database"})
		return
	}
	if deleted == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": fmt.Sprintf("no %s database found", kind)})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":       fmt.Sprintf("%s database deleted", kind),
		"files_deleted": deleted,
	})
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

// publishResult carries the post-publish facts the handlers echo.
type publishResult struct {
	fileID string
	size   int64
	sha256 string
}

// geoipBucket builds a GridFS bucket handle on the `geoip` bucket.
// Cheap — just a struct; constructed per request.
func (handler *AppHandler) geoipBucket() (*gridfs.Bucket, error) {
	return gridfs.NewBucket(handler.Context.Client, options.GridFSBucket().SetName(geoipBucketName))
}

// publishMMDB writes data to the geoip bucket with the mandatory
// metadata.kind + metadata.sha256 (collector contract), then prunes
// every older file of the same kind — the collector only ever reads
// the newest, and GridFS keeps every upload, so stale ~80 MB blobs
// would otherwise pile up.
func (handler *AppHandler) publishMMDB(ctx context.Context, kind string, data []byte, source, uploadedBy string) (publishResult, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if uploadedBy == "" {
		uploadedBy = "unknown"
	}

	bucket, err := handler.geoipBucket()
	if err != nil {
		return publishResult{}, err
	}
	stream, err := bucket.OpenUploadStream(kind+".mmdb", options.GridFSUpload().SetMetadata(bson.M{
		"kind":        kind,
		"sha256":      sha,
		"source":      source,
		"uploaded_by": uploadedBy,
		"uploaded_at": time.Now().UTC(),
	}))
	if err != nil {
		return publishResult{}, fmt.Errorf("open gridfs upload: %w", err)
	}
	if _, err := stream.Write(data); err != nil {
		_ = stream.Abort()
		return publishResult{}, fmt.Errorf("write gridfs stream: %w", err)
	}
	if err := stream.Close(); err != nil {
		return publishResult{}, fmt.Errorf("close gridfs stream: %w", err)
	}

	fileID := ""
	if oid, ok := stream.FileID.(primitive.ObjectID); ok {
		fileID = oid.Hex()
	}

	// Prune older files of this kind. A prune failure is logged, not
	// fatal — the new file is already published and usable; the worst
	// case is wasted GridFS space.
	if pruned, perr := pruneOldGeoIPFiles(ctx, bucket, kind); perr != nil {
		handler.Logger.Warnf("geoip prune (%s) failed: %v", kind, perr)
	} else if pruned > 0 {
		handler.Logger.Infof("geoip prune (%s): removed %d stale file(s)", kind, pruned)
	}

	return publishResult{fileID: fileID, size: int64(len(data)), sha256: sha}, nil
}

// validateMMDB confirms data is a real MaxMind DB and that its
// database_type matches the requested kind. maxminddb.FromBytes
// parses the metadata section; a corrupt / non-MMDB file fails here.
func validateMMDB(data []byte, kind string) error {
	if len(data) == 0 {
		return errors.New("uploaded file is empty")
	}
	r, err := maxminddb.FromBytes(data)
	if err != nil {
		return fmt.Errorf("not a valid MaxMind database: %w", err)
	}
	defer r.Close()

	dbType := r.Metadata.DatabaseType
	detected := detectGeoIPKind(dbType)
	if detected == "" {
		return fmt.Errorf("unrecognised database_type %q — expected a City or ASN database", dbType)
	}
	if detected != kind {
		return fmt.Errorf("file is a %s database (database_type=%q) but kind=%s was requested", detected, dbType, kind)
	}
	return nil
}

// detectGeoIPKind classifies an MMDB database_type into city / asn.
// Covers MaxMind (GeoIP2-City, GeoLite2-ASN, …) and db-ip
// (DBIP-City-Lite, DBIP-ASN-Lite) naming. Returns "" when neither
// token is present.
func detectGeoIPKind(dbType string) string {
	lower := strings.ToLower(dbType)
	switch {
	case strings.Contains(lower, "asn"):
		return geoipKindASN
	case strings.Contains(lower, "city"), strings.Contains(lower, "country"):
		// Country databases are accepted under "city" — the collector's
		// geoip enricher reads whatever city/country DB is mounted.
		return geoipKindCity
	default:
		return ""
	}
}

// normaliseGeoIPKind validates + lowercases a kind parameter.
func normaliseGeoIPKind(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case geoipKindCity:
		return geoipKindCity, true
	case geoipKindASN:
		return geoipKindASN, true
	default:
		return "", false
	}
}

// latestGeoIPFile returns the newest GridFS file for a kind, or nil.
func latestGeoIPFile(ctx context.Context, files *mongo.Collection, kind string) (*geoipFileMeta, error) {
	var m geoipFileMeta
	err := files.FindOne(ctx,
		bson.M{"metadata.kind": kind},
		options.FindOne().SetSort(bson.D{{Key: "uploadDate", Value: -1}}),
	).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// pruneOldGeoIPFiles keeps only the newest file of the kind (by
// uploadDate) and deletes the rest.
//
// "Newest survives" — NOT "everything except the file I just wrote":
// if two uploads for the same kind race, a keep-by-id prune would
// have each one delete the other's file, leaving the kind with no
// database at all. Keeping whichever upload landed last matches what
// the collector itself reads (latest uploadDate), so a race just
// converges on one file instead of zero.
func pruneOldGeoIPFiles(ctx context.Context, bucket *gridfs.Bucket, kind string) (int, error) {
	files := bucket.GetFilesCollection()
	cur, err := files.Find(ctx,
		bson.M{"metadata.kind": kind},
		options.Find().SetSort(bson.D{{Key: "uploadDate", Value: -1}}),
	)
	if err != nil {
		return 0, err
	}
	var docs []geoipFileMeta
	if err := cur.All(ctx, &docs); err != nil {
		return 0, err
	}
	if len(docs) <= 1 {
		return 0, nil // nothing stale
	}
	pruned := 0
	for _, d := range docs[1:] { // docs[0] = newest, kept
		if err := bucket.DeleteContext(ctx, d.ID); err != nil {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}

// deleteAllGeoIPFiles removes every file of the kind. Returns the
// count deleted.
func deleteAllGeoIPFiles(ctx context.Context, bucket *gridfs.Bucket, kind string) (int, error) {
	files := bucket.GetFilesCollection()
	cur, err := files.Find(ctx, bson.M{"metadata.kind": kind})
	if err != nil {
		return 0, err
	}
	var docs []geoipFileMeta
	if err := cur.All(ctx, &docs); err != nil {
		return 0, err
	}
	deleted := 0
	for _, d := range docs {
		if err := bucket.DeleteContext(ctx, d.ID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// readCapped reads at most max+1 bytes; if the source exceeds max it
// returns an error rather than letting an oversized stream exhaust
// memory.
func readCapped(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file exceeds the %d MiB limit", max>>20)
	}
	return data, nil
}

// downloadDBIPLite fetches the free db-ip Lite MMDB for a kind. db-ip
// publishes a fresh file at the start of each month; the current
// month may not be live yet, so we try this month then fall back to
// the previous one. The gzip payload is decompressed in memory.
func downloadDBIPLite(ctx context.Context, kind string) ([]byte, string, error) {
	client := &http.Client{Timeout: dbipDownloadTimeout}
	now := time.Now().UTC()
	months := []time.Time{now, now.AddDate(0, -1, 0)}

	var lastErr error
	for _, m := range months {
		url := fmt.Sprintf("https://download.db-ip.com/free/dbip-%s-lite-%s.mmdb.gz", kind, m.Format("2006-01"))
		data, err := fetchGzip(ctx, client, url)
		if err == nil {
			return data, url, nil
		}
		lastErr = err
	}
	return nil, "", fmt.Errorf("db-ip Lite %s not available for the last two months: %w", kind, lastErr)
}

// fetchGzip GETs a .gz URL and returns the decompressed body, capped
// at maxMMDBSize.
func fetchGzip(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	return readCapped(gz, maxMMDBSize)
}
