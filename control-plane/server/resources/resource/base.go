package resource

import (
	"context"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/resources/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type AllResources struct {
	*common.Resources
	mutex sync.RWMutex
}

func NewResources() *AllResources {
	return &AllResources{
		Resources: &common.Resources{},
	}
}

func GenerateSnapshot(ctx context.Context, rawListenerResource *models.DBResource, listenerName string, db *db.AppContext, logger *logrus.Logger, project, version, downstreamAddress string) (*AllResources, error) {
	ar := NewResources()
	var nodeID string

	if downstreamAddress != "" {
		nodeID = fmt.Sprintf("%s::%s::%s", listenerName, project, downstreamAddress)
	} else {
		nodeID = fmt.Sprintf("%s::%s", listenerName, project)
	}

	ar.mutex.Lock()
	ar.SetNodeID(nodeID)
	ar.SetResourceVersion(version)
	ar.mutex.Unlock()
	ar.DecodeListener(ctx, rawListenerResource, db, logger, downstreamAddress)
	return ar, nil
}
