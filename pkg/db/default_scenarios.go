package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func CreateDefaultScenarios(ctx context.Context, db *AppContext) error {

	collection := db.Client.Collection("scenarios")

	// Define default dynamic scenarios with selected fields
	defaultScenarios := []models.Scenario{
		{
			ID:          primitive.NewObjectID(),
			Name:        "Basic HTTP Service",
			Description: "Simple HTTP service with static cluster, HCM, and listener",
			ScenarioID:  "basic-http",
			Components: []models.ComponentInstance{
				{
					Type: "cluster",
					Name: "basic-cluster",
					Priority: 1,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "basic-cluster"},
						{FieldName: "type", Required: true, Value: "STATIC"},
						{FieldName: "connect_timeout", Required: false, Value: "2s"},
						{FieldName: "lb_policy", Required: false, Value: "ROUND_ROBIN"},
						{
							FieldName: "endpoint_discovery_config", 
							Required: true,
							NestedSelection: &models.NestedFieldSelection{
								SelectedChoice: "static_endpoints",
								SubFields: []models.SelectedField{
									{FieldName: "endpoints", Required: true, Value: []interface{}{
										map[string]interface{}{
											"address": "httpbin.org",
											"port": 80,
											"protocol": "TCP",
										},
									}},
								},
							},
						},
					},
				},
				{
					Type: "http_connection_manager", 
					Name: "basic-hcm",
					Priority: 2,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "basic-hcm"},
						{FieldName: "stat_prefix", Required: true, Value: "ingress_http"},
						{FieldName: "codec_type", Required: true, Value: "AUTO"},
						{
							FieldName: "route_configuration", 
							Required: true,
							NestedSelection: &models.NestedFieldSelection{
								SelectedChoice: "inline",
								SubFields: []models.SelectedField{
									{FieldName: "name", Required: true, Value: "basic-route-config"},
									{
										FieldName: "virtual_host_config",
										Required: true,
										NestedSelection: &models.NestedFieldSelection{
											SelectedChoice: "inline_virtual_hosts",
											SubFields: []models.SelectedField{
												{FieldName: "virtual_hosts", Required: true, Value: []interface{}{
													map[string]interface{}{
														"name": "basic-vhost",
														"domains": []string{"*"},
														"routes": []interface{}{
															map[string]interface{}{
																"match_type": "prefix",
																"match_value": "/",
																"route_cluster": "basic-cluster",
																"timeout": "15s",
																"retry_policy_enabled": false,
															},
														},
													},
												}},
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Type: "listener",
					Name: "http-listener",
					Priority: 3,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "http-listener"},
						{FieldName: "address", Required: true, Value: "0.0.0.0"},
						{FieldName: "port", Required: true, Value: 8080},
						{FieldName: "protocol", Required: true, Value: "TCP"},
						{FieldName: "network_filters", Required: false, Value: []interface{}{
							map[string]interface{}{
								"name": "basic-hcm",
								"gtype": "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
							},
						}},
					},
				},
			},
			IsDefault:   true,
			CreatedBy:   "system",
			Project:     nil, // Available to all projects
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:          primitive.NewObjectID(),
			Name:        "HTTP Service with EDS",
			Description: "HTTP service using Endpoint Discovery Service",
			ScenarioID:  "http-eds",
			Components: []models.ComponentInstance{
				{
					Type: "endpoint",
					Name: "service-endpoints",
					Priority: 1,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "service-endpoints"},
						{FieldName: "endpoints", Required: true, Value: []interface{}{
							map[string]interface{}{
								"address": "httpbin.org",
								"port": 80,
								"protocol": "TCP",
							},
							map[string]interface{}{
								"address": "httpbin.org",
								"port": 443,
								"protocol": "TCP",
							},
						}},
					},
				},
				{
					Type: "cluster",
					Name: "eds-cluster",
					Priority: 2,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "eds-cluster"},
						{FieldName: "type", Required: true, Value: "EDS"},
						{FieldName: "connect_timeout", Required: false, Value: "2s"},
						{FieldName: "lb_policy", Required: false, Value: "ROUND_ROBIN"},
						{
							FieldName: "endpoint_discovery_config", 
							Required: true,
							NestedSelection: &models.NestedFieldSelection{
								SelectedChoice: "eds",
								SubFields: []models.SelectedField{
									{FieldName: "eds_service_name", Required: true, Value: "service-endpoints"},
								},
							},
						},
					},
				},
				{
					Type: "http_connection_manager",
					Name: "eds-hcm",
					Priority: 3, 
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "eds-hcm"},
						{FieldName: "stat_prefix", Required: true, Value: "ingress_http"},
						{FieldName: "codec_type", Required: true, Value: "AUTO"},
						{
							FieldName: "route_configuration", 
							Required: true,
							NestedSelection: &models.NestedFieldSelection{
								SelectedChoice: "inline",
								SubFields: []models.SelectedField{
									{FieldName: "name", Required: true, Value: "eds-route-config"},
									{
										FieldName: "virtual_host_config",
										Required: true,
										NestedSelection: &models.NestedFieldSelection{
											SelectedChoice: "inline_virtual_hosts",
											SubFields: []models.SelectedField{
												{FieldName: "virtual_hosts", Required: true, Value: []interface{}{
													map[string]interface{}{
														"name": "eds-vhost",
														"domains": []string{"*"},
														"routes": []interface{}{
															map[string]interface{}{
																"match_type": "prefix",
																"match_value": "/",
																"route_cluster": "eds-cluster",
																"timeout": "15s",
																"retry_policy_enabled": true,
																"retry_on": "5xx,gateway-error,connect-failure",
																"num_retries": 3,
															},
														},
													},
												}},
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Type: "listener",
					Name: "eds-listener",
					Priority: 4,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "eds-listener"},
						{FieldName: "address", Required: true, Value: "0.0.0.0"},
						{FieldName: "port", Required: true, Value: 8080},
						{FieldName: "protocol", Required: true, Value: "TCP"},
						{FieldName: "network_filters", Required: false, Value: []interface{}{
							map[string]interface{}{
								"name": "eds-hcm",
								"gtype": "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
							},
						}},
					},
				},
			},
			IsDefault:   true,
			CreatedBy:   "system",
			Project:     nil, // Available to all projects
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:          primitive.NewObjectID(),
			Name:        "Basic TCP Service",
			Description: "Simple TCP service with static cluster and TCP proxy",
			ScenarioID:  "basic-tcp",
			Components: []models.ComponentInstance{
				{
					Type: "cluster",
					Name: "tcp-cluster",
					Priority: 1,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "tcp-cluster"},
						{FieldName: "type", Required: true, Value: "STRICT_DNS"},
						{FieldName: "connect_timeout", Required: false, Value: "2s"},
						{FieldName: "lb_policy", Required: false, Value: "ROUND_ROBIN"},
						{
							FieldName: "endpoint_discovery_config", 
							Required: true,
							NestedSelection: &models.NestedFieldSelection{
								SelectedChoice: "static_endpoints",
								SubFields: []models.SelectedField{
									{FieldName: "endpoints", Required: true, Value: []interface{}{
										map[string]interface{}{
											"address": "127.0.0.1",
											"port": 9090,
											"protocol": "TCP",
										},
									}},
								},
							},
						},
					},
				},
				{
					Type: "tcp_proxy",
					Name: "tcp-proxy",
					Priority: 2,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "tcp-proxy"},
						{FieldName: "stat_prefix", Required: true, Value: "tcp_proxy"},
						{FieldName: "cluster", Required: true, Value: "tcp-cluster"},
					},
				},
				{
					Type: "listener",
					Name: "tcp-listener",
					Priority: 3,
					SelectedFields: []models.SelectedField{
						{FieldName: "name", Required: true, Value: "tcp-listener"},
						{FieldName: "address", Required: true, Value: "0.0.0.0"},
						{FieldName: "port", Required: true, Value: 9999},
						{FieldName: "protocol", Required: true, Value: "TCP"},
						{FieldName: "network_filters", Required: false, Value: []interface{}{
							map[string]interface{}{
								"name": "tcp-proxy",
								"gtype": "envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
							},
						}},
					},
				},
			},
			IsDefault:   true,
			CreatedBy:   "system",
			Project:     nil, // Available to all projects
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	// Insert each scenario if it doesn't exist
	for _, scenario := range defaultScenarios {
		filter := bson.M{
			"scenario_id": scenario.ScenarioID,
		}

		var existing models.Scenario
		err := collection.FindOne(ctx, filter).Decode(&existing)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				// Scenario doesn't exist, create it
				_, insertErr := collection.InsertOne(ctx, scenario)
				if insertErr != nil {
					return fmt.Errorf("failed to insert scenario %s: %w", scenario.ScenarioID, insertErr)
				}
				fmt.Printf("Created default dynamic scenario: %s\n", scenario.Name)
			} else {
				return fmt.Errorf("failed to check scenario %s: %w", scenario.ScenarioID, err)
			}
		}
	}

	return nil
}