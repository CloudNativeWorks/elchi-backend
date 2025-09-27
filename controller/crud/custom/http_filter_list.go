package custom

import (
	"context"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// sanitizeHTTPFilterInput sanitizes HTTP filter input to prevent ReDoS attacks
func sanitizeHTTPFilterInput(input string) string {
	// Basic cleanup
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	// Length limit
	if len(input) > 50 {
		input = input[:50]
	}

	// Check for suspicious patterns
	suspiciousPatterns := []string{
		`\(\w\+\)\+`, // (a+)+
		`\w\*\w\*`,   // a*b*
		`\[\^\]\*`,   // [^]*
		`\(\.\*\)\+`, // (.*)+
	}

	for _, pattern := range suspiciousPatterns {
		if matched, _ := regexp.MatchString(pattern, input); matched {
			return ""
		}
	}

	// Escape regex special characters for literal matching
	return regexp.QuoteMeta(input)
}

func (custom *AppHandler) GetCustomHTTPFilterList(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	collection := custom.Context.Client.Collection(requestDetails.Collection)
	opts := options.Find()
	opts.SetProjection(bson.M{
		"general.name":           1,
		"general.canonical_name": 1,
		"general.gtype":          1,
		"general.type":           1,
		"general.category":       1,
	})

	// Project is required - if no project specified, return empty result
	if requestDetails.Project == "" {
		return []Record{}, nil
	}

	filters := bson.M{
		"general.version": requestDetails.Version,
		"general.project": requestDetails.Project,
	}

	// Only add category filter if category is provided
	if requestDetails.Category != "" {
		filters["general.category"] = requestDetails.Category
	}

	// Only add metadata.http_filter filter if it's provided (with sanitization)
	if httpFilter, ok := requestDetails.Metadata["http_filter"]; ok && httpFilter != "" {
		// Sanitize the http_filter input to prevent ReDoS attacks
		sanitizedFilter := sanitizeHTTPFilterInput(httpFilter)
		if sanitizedFilter != "" {
			filters["general.metadata.http_filter"] = bson.M{"$regex": sanitizedFilter, "$options": "i"}
		}
	}

	if requestDetails.CanonicalName != "" {
		filters["general.canonical_name"] = requestDetails.CanonicalName
	}

	filters = common.AddUserFilter(requestDetails, filters)

	cursor, err := collection.Find(ctx, filters, opts)
	if err != nil {
		return nil, errstr.ErrUnknownDBError
	}

	var results []Record
	for cursor.Next(ctx) {
		var doc struct {
			General struct {
				Name          string `bson:"name"`
				CanonicalName string `bson:"canonical_name"`
				GType         string `bson:"gtype"`
				Type          string `bson:"type"`
				Category      string `bson:"category"`
				Collection    string `bson:"collection"`
			} `bson:"general"`
		}

		if err := cursor.Decode(&doc); err != nil {
			custom.Logger.Debugf("Error decoding http filter: %v", err)
		}

		results = append(
			results,
			Record{
				Name:          doc.General.Name,
				CanonicalName: doc.General.CanonicalName,
				GType:         doc.General.GType,
				Type:          doc.General.Type,
				Category:      doc.General.Category,
				Collection:    requestDetails.Collection,
			},
		)
	}

	if err := cursor.Err(); err != nil {
		custom.Logger.Debug(err)
	}

	return results, nil
}
