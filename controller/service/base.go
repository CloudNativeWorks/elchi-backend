package service

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type AppHandler struct {
	Context *db.AppContext
	XDS     *xds.AppHandler
	Logger  *logger.Logger
}

type Service struct {
	ID        primitive.ObjectID `json:"id" bson:"_id"`
	Name      string             `json:"name" bson:"name"`
	Project   string             `json:"project" bson:"project"`
	AdminPort uint32             `json:"admin_port" bson:"admin_port"`
	Clients   []Client           `json:"clients" bson:"clients"`
}

type Client struct {
	DownstreamAddress string `json:"downstream_address" bson:"downstream_address"`
	ClientID          string `json:"client_id" bson:"client_id"`
}

func NewServiceHandler(context *db.AppContext) *AppHandler {
	xdsHandler := xds.NewXDSHandler(context)
	return &AppHandler{
		Context: context,
		XDS:     xdsHandler,
		Logger:  logger.NewLogger("controller/service"),
	}
}
