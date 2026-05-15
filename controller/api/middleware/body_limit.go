package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxBackupBodyBytes caps the size of /backup/* request payloads. Backup
// import payloads carry every XDS resource of a project; this ceiling
// protects the controller from arbitrary multi-GB JSON uploads that
// would otherwise be unmarshalled fully in-memory (ShouldBindJSON has
// no built-in streaming) and risk an OOM kill. 100 MB is comfortably
// above realistic backups (~50 MB for ~1000 listeners + deps) while
// keeping a hostile request well within recoverable memory.
const MaxBackupBodyBytes = 100 * 1024 * 1024

// BodyLimitMiddleware wraps the request body with http.MaxBytesReader
// so every downstream read (auth pre-parse, handler bind) aborts as
// soon as the byte ceiling is exceeded. The 413 response is returned
// directly so handlers do not need to special-case the error.
func BodyLimitMiddleware(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)

		// Probing read isn't safe here (would consume the body before the
		// handler sees it); instead, downstream readers surface the
		// http.MaxBytesError on first oversized read. We intercept that
		// shape via a deferred error check below.
		c.Next()

		// If a downstream binder already responded, do nothing.
		if c.Writer.Written() {
			return
		}
		// Surface the canonical 413 if the request was aborted with the
		// MaxBytesError without anyone responding (rare, but keeps the
		// contract uniform).
		for _, ginErr := range c.Errors {
			var maxBytesErr *http.MaxBytesError
			if errors.As(ginErr.Err, &maxBytesErr) {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
					"error":          "request body too large",
					"max_bytes":      maxBytes,
					"received_bytes": maxBytesErr.Limit,
				})
				return
			}
		}
	}
}
