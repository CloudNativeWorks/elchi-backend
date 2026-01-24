package gslb

import (
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// MakeIPKey creates a unique key for an IP within a GSLB record
// Format: "recordID:ip" (e.g., "507f1f77bcf86cd799439011:192.168.1.1")
// Used consistently across CounterManager, TimeWheel, and HealthChecker
func MakeIPKey(recordID primitive.ObjectID, ip string) string {
	return fmt.Sprintf("%s:%s", recordID.Hex(), ip)
}
