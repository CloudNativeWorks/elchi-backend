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
	GetCommandInterfaces() []*pb.Interfaces
	GetCommandFRRType() *pb.FrrProtocolType
	GetCommandBGP() *pb.RequestBgp

	GetClients() []ServiceClients
	GetExtend() Extend

	AppendClient(ServiceClients)
	SetExtend(Extend)
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
	Interfaces []*pb.Interfaces     `json:"interfaces,omitempty"`
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

func (o *Operations) GetCommandInterfaces() []*pb.Interfaces {
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
