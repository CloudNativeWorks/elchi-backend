package bridge

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
)

func ipv4Dialer(ctx context.Context, addr string) (net.Conn, error) {
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "tcp4", addr)
}

func NewGRPCClient(appCtx *db.AppContext) (*grpc.ClientConn, error) {
	var transportCredentials credentials.TransportCredentials

	if appCtx.Config.ElchiInternalCommunication == "true" {
		transportCredentials = insecure.NewCredentials()
	} else if appCtx.Config.ElchiTLSEnabled == "true" {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: true,
		}
		transportCredentials = credentials.NewTLS(tlsConfig)
	} else {
		transportCredentials = insecure.NewCredentials()
	}

	return grpc.NewClient(
		GetElchiAddressPort(appCtx),
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithContextDialer(ipv4Dialer),
		grpc.WithDisableServiceConfig(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithAuthority(appCtx.Config.ElchiAddress),
		grpc.WithDefaultCallOptions(
			grpc.WaitForReady(true),
		),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  1.0 * time.Second,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   10 * time.Second,
			},
		}),
	)
}

func GetElchiAddressPort(appCtx *db.AppContext) string {
	if appCtx.Config.ElchiInternalCommunication == "true" {
		return appCtx.Config.ElchiInternalAddressPort
	}
	return appCtx.Config.ElchiAddress + ":" + appCtx.Config.ElchiPort
}
