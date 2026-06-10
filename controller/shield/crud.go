package shield

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrPolicyNameTaken is returned when a (name, project) pair already exists.
var ErrPolicyNameTaken = errors.New("a shield policy with this name already exists in the project")

// ErrPolicyNotFound is returned when a policy id does not resolve.
var ErrPolicyNotFound = errors.New("shield policy not found")

// CRUDService handles CRUD on the shield_policies collection.
type CRUDService struct {
	dbContext *db.AppContext
	logger    *logger.Logger
}

// NewCRUDService constructs a shield policy CRUD service.
func NewCRUDService(dbContext *db.AppContext, logger *logger.Logger) *CRUDService {
	return &CRUDService{dbContext: dbContext, logger: logger}
}

func (s *CRUDService) collection() *mongo.Collection {
	return s.dbContext.Client.Collection(ShieldPolicyCollection)
}

// validate checks a request: a non-empty file set, each file with a safe relative
// path and exactly one content source (inline content OR a download URL+sha256).
func validate(req *ShieldPolicyRequest) error {
	if len(req.Files) == 0 && !req.FullSync {
		return fmt.Errorf("files is empty (set full_sync to deploy an empty/clearing bundle)")
	}
	seen := make(map[string]struct{}, len(req.Files))
	for i := range req.Files {
		f := req.Files[i]
		if strings.TrimSpace(f.Path) == "" {
			return fmt.Errorf("files[%d].path is required", i)
		}
		clean := path.Clean(f.Path)
		if path.IsAbs(f.Path) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("files[%d].path %q must be a safe relative path", i, f.Path)
		}
		if _, dup := seen[clean]; dup {
			return fmt.Errorf("files[%d]: duplicate path %q", i, clean)
		}
		seen[clean] = struct{}{}
		if f.DownloadURL != "" {
			if f.Sha256 == "" {
				return fmt.Errorf("files[%d]: a download_url requires a sha256", i)
			}
		} else if len(f.Content) == 0 {
			return fmt.Errorf("files[%d]: either content or download_url is required", i)
		}
	}
	return nil
}

// Create inserts a new policy (version 1). A duplicate (name, project) errors.
func (s *CRUDService) Create(ctx context.Context, req ShieldPolicyRequest) (*ShieldPolicy, error) {
	if err := validate(&req); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	policy := &ShieldPolicy{
		Name:      req.Name,
		Project:   req.Project,
		FullSync:  req.FullSync,
		Files:     req.Files,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	res, err := s.collection().InsertOne(ctx, policy)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrPolicyNameTaken
		}
		return nil, fmt.Errorf("insert shield policy: %w", err)
	}
	policy.ID = res.InsertedID.(primitive.ObjectID)
	return policy, nil
}

// Update replaces a policy's bundle and bumps its version. Returns the updated doc.
func (s *CRUDService) Update(ctx context.Context, id string, req ShieldPolicyRequest) (*ShieldPolicy, error) {
	if err := validate(&req); err != nil {
		return nil, err
	}
	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrPolicyNotFound
	}
	update := bson.M{
		"$set": bson.M{
			"full_sync":  req.FullSync,
			"files":      req.Files,
			"updated_at": time.Now().UTC(),
		},
		"$inc": bson.M{"version": 1},
	}
	var updated ShieldPolicy
	err = s.collection().FindOneAndUpdate(
		ctx,
		bson.M{"_id": objectID, "project": req.Project},
		update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrPolicyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update shield policy: %w", err)
	}
	return &updated, nil
}

// GetByID fetches a policy, scoped to project.
func (s *CRUDService) GetByID(ctx context.Context, id, project string) (*ShieldPolicy, error) {
	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, ErrPolicyNotFound
	}
	var policy ShieldPolicy
	err = s.collection().FindOne(ctx, bson.M{"_id": objectID, "project": project}).Decode(&policy)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrPolicyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get shield policy: %w", err)
	}
	return &policy, nil
}

// List returns the project's policies, newest first.
func (s *CRUDService) List(ctx context.Context, project string) ([]ShieldPolicy, error) {
	cursor, err := s.collection().Find(
		ctx,
		bson.M{"project": project},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list shield policies: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()
	policies := []ShieldPolicy{}
	if err := cursor.All(ctx, &policies); err != nil {
		return nil, fmt.Errorf("decode shield policies: %w", err)
	}
	return policies, nil
}

// Delete removes a policy, scoped to project.
func (s *CRUDService) Delete(ctx context.Context, id, project string) error {
	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return ErrPolicyNotFound
	}
	res, err := s.collection().DeleteOne(ctx, bson.M{"_id": objectID, "project": project})
	if err != nil {
		return fmt.Errorf("delete shield policy: %w", err)
	}
	if res.DeletedCount == 0 {
		return ErrPolicyNotFound
	}
	return nil
}
