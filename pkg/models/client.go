package models

import (
	"encoding/json"
	"fmt"

	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type CommandTypeJSON pb.CommandType
type SubCommandTypeJSON pb.SubCommandType
type FRRProtocolTypeJSON pb.FrrProtocolType

type OperationClass interface {
	GetType() string
	GetTypeNum() pb.CommandType
	GetSubType() string
	GetSubTypeNum() pb.SubCommandType
	GetCommands() Command
	GetCommandProject() string
	GetCommandName() string
	GetCommandRaw() string
	GetCommandCount() uint32
	GetCommandSearch() string
	GetCommandLevels() []string
	GetCommandComponents() []string
	GetCommandMethod() pb.HttpMethod
	GetCommandPath() (string, error)
	GetCommandQueries() map[string]string
	GetCommandInterfaces() []*pb.InterfaceState // Updated to use InterfaceState from network.proto
	GetCommandFRRType() *pb.FrrProtocolType
	GetCommandBGP() *pb.RequestBgp

	// Network-related methods for new proto structure
	GetOperation() *pb.Command
	GetNetplanConfig() *NetplanConfig
	GetRouteOperations() []*RouteOperation
	GetPolicyOperations() []*PolicyOperation
	GetTableOperations() []*TableOperation

	// Envoy version related methods
	GetEnvoyVersion() *pb.RequestEnvoyVersion

	GetClients() []ServiceClients
	GetExtend() Extend

	AppendClient(ServiceClients)
	SetExtend(Extend)
}

// Network operation structures for new proto format
type NetplanConfig struct {
	YamlContent                  string `json:"yaml_content"`
	TestMode                     bool   `json:"test_mode"`
	TestTimeoutSeconds           uint32 `json:"test_timeout_seconds"`
	PreserveControllerConnection bool   `json:"preserve_controller_connection"`
}

// Use proto structs directly instead of duplicating
type RouteOperation struct {
	Action string    `json:"action"` // ADD, DELETE, REPLACE
	Route  *pb.Route `json:"route"`
}

type PolicyOperation struct {
	Action string            `json:"action"` // ADD, DELETE, REPLACE
	Policy *pb.RoutingPolicy `json:"policy"`
}

type TableOperation struct {
	Action string                      `json:"action"` // ADD, DELETE, REPLACE
	Table  *pb.RoutingTableDefinition `json:"table"`
}

// JSON wrapper for RequestEnvoyVersion to handle string->enum conversion
type RequestEnvoyVersionJSON struct {
	Operation     string `json:"operation"`      // String from JSON: "GET_VERSIONS", "SET_VERSION"
	Version       string `json:"version"`        // Required for SET_VERSION
	ForceDownload bool   `json:"force_download"` // Optional for SET_VERSION
}

// Convert to protobuf struct
func (r *RequestEnvoyVersionJSON) ToPB() *pb.RequestEnvoyVersion {
	if r == nil {
		return nil
	}

	var operation pb.EnvoyVersionOperation
	switch r.Operation {
	case "GET_VERSIONS":
		operation = pb.EnvoyVersionOperation_GET_VERSIONS
	case "SET_VERSION":
		operation = pb.EnvoyVersionOperation_SET_VERSION
	default:
		operation = pb.EnvoyVersionOperation_GET_VERSIONS // default fallback
	}

	return &pb.RequestEnvoyVersion{
		Operation:     operation,
		Version:       r.Version,
		ForceDownload: r.ForceDownload,
	}
}


type AllowedProxyPaths string

const (
	Logging  AllowedProxyPaths = "/logging"
	Clusters AllowedProxyPaths = "/clusters"
	Envoy    AllowedProxyPaths = "/envoy"

	HCOK          AllowedProxyPaths = "/healthcheck/ok"
	HCFAIL        AllowedProxyPaths = "/healthcheck/fail"
	ResetCounters AllowedProxyPaths = "/reset_counters"
	ReopenLogs    AllowedProxyPaths = "/reopen_logs"
	RuntimeModify AllowedProxyPaths = "/runtime_modify"
)

var allowedProxyPathsList = []AllowedProxyPaths{
	Logging, HCOK, HCFAIL, Clusters, Envoy,
	ResetCounters, ReopenLogs, RuntimeModify,
}

type Operations struct {
	Type    CommandTypeJSON    `json:"type"`
	SubType SubCommandTypeJSON `json:"sub_type,omitempty"`
	Clients []ServiceClients   `json:"clients"`
	Command Command            `json:"command"`
	Extend  *Extend            `json:"extend,omitempty"`
	
	// Network operation fields
	NetplanConfig    *NetplanConfig     `json:"netplan_config,omitempty"`
	RouteOperations  []*RouteOperation  `json:"route_operations,omitempty"`
	PolicyOperations []*PolicyOperation `json:"policy_operations,omitempty"`
	TableOperations  []*TableOperation  `json:"table_operations,omitempty"`

	// Envoy version operation field
	EnvoyVersionOp *RequestEnvoyVersionJSON `json:"envoy_version,omitempty"`
}

type ServiceClients struct {
	ClientID          string `json:"client_id" bson:"client_id"`
	DownstreamAddress string `json:"downstream_address" bson:"downstream_address"`
}

type RequestBgpJSON struct {
	Operation     string               `json:"operation,omitempty"`
	Config        *pb.BgpConfig        `json:"config,omitempty"`
	Neighbor      *pb.BgpNeighbor      `json:"neighbor,omitempty"`
	PeerIp        string               `json:"peer_ip,omitempty"`
	NetworkPrefix string               `json:"network_prefix,omitempty"`
	RouteMap      *pb.BgpRouteMap      `json:"route_map,omitempty"`
	CommunityList *pb.BgpCommunityList `json:"community_list,omitempty"`
	PrefixList    *pb.BgpPrefixList    `json:"prefix_list,omitempty"`
	Community     string               `json:"community,omitempty"`
	AsNumber      uint32               `json:"as_number,omitempty"`
	LocalAs       uint32               `json:"local_as,omitempty"`     // New field
	RemoteAs      uint32               `json:"remote_as,omitempty"`    // New field
	Clear         *pb.ClearBgp         `json:"clear,omitempty"`
}

func (r *RequestBgpJSON) ToPB() *pb.RequestBgp {
	if r == nil {
		return nil
	}

	operation, ok := pb.BgpOperationType_value[r.Operation]
	if !ok {
		return nil
	}

	return &pb.RequestBgp{
		Operation:     pb.BgpOperationType(operation),
		Config:        r.Config,
		Neighbor:      r.Neighbor,
		PeerIp:        r.PeerIp,
		RouteMap:      r.RouteMap,
		CommunityList: r.CommunityList,
		PrefixList:    r.PrefixList,
		AsNumber:      r.AsNumber,
		LocalAs:       r.LocalAs,       // New field
		RemoteAs:      r.RemoteAs,      // New field
		Clear:         r.Clear,
	}
}

type Command struct {
	Project    string               `json:"project,omitempty"`
	Name       string               `json:"name,omitempty"`
	Count      uint32               `json:"count,omitempty"`
	Method     string               `json:"method,omitempty"`
	Path       AllowedProxyPaths    `json:"path,omitempty"`
	Queries    map[string]string    `json:"queries,omitempty"`
	Raw        string               `json:"raw,omitempty"`
	Search     string               `json:"search,omitempty"`
	Levels     []string             `json:"levels,omitempty"`
	Components []string             `json:"components,omitempty"`
	Interfaces []*pb.InterfaceState `json:"interfaces,omitempty"`
	Protocol   *FRRProtocolTypeJSON `json:"protocol,omitempty"`
	Bgp        *RequestBgpJSON      `json:"bgp,omitempty"`
}

type Extend struct {
	DownstreamAddress string `json:"downstream_address,omitempty"`
	Port              uint32 `json:"port,omitempty"`
}

type ClientFields struct {
	DownstreamAddress string
	ClientName        string
}

func (o *Operations) GetType() string {
	return o.Type.String()
}

func (o *Operations) GetSubType() string {
	return o.SubType.String()
}

func (o *Operations) GetCommands() Command {
	return o.Command
}

func (c CommandTypeJSON) String() string {
	return pb.CommandType(c).String()
}

func (s SubCommandTypeJSON) String() string {
	return pb.SubCommandType(s).String()
}

func (f FRRProtocolTypeJSON) String() string {
	return pb.FrrProtocolType(f).String()
}

func (o *Operations) GetTypeNum() pb.CommandType {
	return pb.CommandType(o.Type)
}

func (o *Operations) GetCommandCount() uint32 {
	return o.Command.Count
}

func (o *Operations) GetCommandSearch() string {
	return o.Command.Search
}

func (o *Operations) GetCommandLevels() []string {
	return o.Command.Levels
}

func (o *Operations) GetCommandComponents() []string {
	return o.Command.Components
}

func (o *Operations) GetSubTypeNum() pb.SubCommandType {
	return pb.SubCommandType(o.SubType)
}

func (o *Operations) GetCommandProject() string {
	return o.Command.Project
}

func (o *Operations) GetCommandName() string {
	return o.Command.Name
}

func (o *Operations) GetCommandMethod() pb.HttpMethod {
	return pb.HttpMethod(pb.HttpMethod_value[o.Command.Method])
}

func IsAllowedProxyPath(path AllowedProxyPaths) bool {
	for _, allowed := range allowedProxyPathsList {
		if path == allowed {
			return true
		}
	}
	return false
}

func (o *Operations) GetCommandPath() (string, error) {
	if IsAllowedProxyPath(o.Command.Path) {
		return string(o.Command.Path), nil
	}
	return "", fmt.Errorf("path is not supported")
}

func (o *Operations) GetCommandQueries() map[string]string {
	return o.Command.Queries
}

func (o *Operations) GetCommandInterfaces() []*pb.InterfaceState {
	return o.Command.Interfaces
}

func (o *Operations) GetCommandFRRType() *pb.FrrProtocolType {
	if o.Command.Protocol != nil {
		val := pb.FrrProtocolType(*o.Command.Protocol)
		return &val
	}
	return nil
}

func (o *Operations) GetCommandBGP() *pb.RequestBgp {
	return o.Command.Bgp.ToPB()
}

func (o *Operations) GetCommandRaw() string {
	return o.Command.Raw
}

func (o *Operations) GetClients() []ServiceClients {
	return o.Clients
}

func (o *Operations) AppendClient(client ServiceClients) {
	o.Clients = append(o.Clients, client)
}

func (o *Operations) GetExtend() Extend {
	return *o.Extend
}

func (o *Operations) SetExtend(extend Extend) {
	o.Extend = &extend
}

func (c *CommandTypeJSON) UnmarshalJSON(data []byte) error {
	var strValue string
	if err := json.Unmarshal(data, &strValue); err != nil {
		return err
	}

	enumVal, ok := pb.CommandType_value[strValue]
	if !ok {
		return fmt.Errorf("invalid CommandType value: %s", strValue)
	}

	*c = CommandTypeJSON(enumVal)
	return nil
}

func (s *SubCommandTypeJSON) UnmarshalJSON(data []byte) error {
	var strValue string
	if err := json.Unmarshal(data, &strValue); err != nil {
		return err
	}

	enumVal, ok := pb.SubCommandType_value[strValue]
	if !ok {
		return fmt.Errorf("invalid SubCommandType value: %s", strValue)
	}

	*s = SubCommandTypeJSON(enumVal)
	return nil
}

func (f *FRRProtocolTypeJSON) UnmarshalJSON(data []byte) error {
	var strValue string
	if err := json.Unmarshal(data, &strValue); err != nil {
		return err
	}

	enumVal, ok := pb.FrrProtocolType_value[strValue]
	if !ok {
		return fmt.Errorf("invalid FrrProtocolType value: %s", strValue)
	}

	*f = FRRProtocolTypeJSON(enumVal)
	return nil
}

// Network operation methods implementation
func (o *Operations) GetOperation() *pb.Command {
	// Build pb.Command from operations data
	command := &pb.Command{
		Type:    o.GetTypeNum(),
		SubType: o.GetSubTypeNum(),
	}
	
	// Add network request data if present
	if o.NetplanConfig != nil || len(o.RouteOperations) > 0 || len(o.PolicyOperations) > 0 || len(o.TableOperations) > 0 {
		network := &pb.RequestNetwork{}
		
		if o.NetplanConfig != nil {
			network.NetplanConfig = &pb.NetplanConfig{
				YamlContent:                  o.NetplanConfig.YamlContent,
				TestMode:                     o.NetplanConfig.TestMode,
				TestTimeoutSeconds:           o.NetplanConfig.TestTimeoutSeconds,
				PreserveControllerConnection: o.NetplanConfig.PreserveControllerConnection,
			}
		}
		
		// Convert route operations
		if len(o.RouteOperations) > 0 {
			for _, routeOp := range o.RouteOperations {
				var action pb.RouteOperation_Action
				switch routeOp.Action {
				case "ADD":
					action = pb.RouteOperation_ADD
				case "DELETE":
					action = pb.RouteOperation_DELETE
				case "REPLACE":
					action = pb.RouteOperation_REPLACE
				}
				
				network.RouteOperations = append(network.RouteOperations, &pb.RouteOperation{
					Action: action,
					Route:  routeOp.Route, // Direct proto struct usage
				})
			}
		}
		
		// Convert policy operations
		if len(o.PolicyOperations) > 0 {
			for _, policyOp := range o.PolicyOperations {
				var action pb.RoutingPolicyOperation_Action
				switch policyOp.Action {
				case "ADD":
					action = pb.RoutingPolicyOperation_ADD
				case "DELETE":
					action = pb.RoutingPolicyOperation_DELETE
				case "REPLACE":
					action = pb.RoutingPolicyOperation_REPLACE
				}
				
				network.PolicyOperations = append(network.PolicyOperations, &pb.RoutingPolicyOperation{
					Action: action,
					Policy: policyOp.Policy, // Direct proto struct usage
				})
			}
		}
		
		// Convert table operations
		if len(o.TableOperations) > 0 {
			for _, tableOp := range o.TableOperations {
				var action pb.TableOperation_Action
				switch tableOp.Action {
				case "ADD":
					action = pb.TableOperation_ADD
				case "DELETE":
					action = pb.TableOperation_DELETE
				case "REPLACE":
					action = pb.TableOperation_REPLACE
				}
				
				network.TableOperations = append(network.TableOperations, &pb.TableOperation{
					Action: action,
					Table:  tableOp.Table, // Direct proto struct usage
				})
			}
		}
		
		// Set network payload in the command using oneof
		command.Payload = &pb.Command_Network{
			Network: network,
		}
	}
	
	return command
}

func (o *Operations) GetNetplanConfig() *NetplanConfig {
	return o.NetplanConfig
}

func (o *Operations) GetRouteOperations() []*RouteOperation {
	return o.RouteOperations
}

func (o *Operations) GetPolicyOperations() []*PolicyOperation {
	return o.PolicyOperations
}

func (o *Operations) GetTableOperations() []*TableOperation {
	return o.TableOperations
}

func (o *Operations) GetEnvoyVersion() *pb.RequestEnvoyVersion {
	return o.EnvoyVersionOp.ToPB()
}
