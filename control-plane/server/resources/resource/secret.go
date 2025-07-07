package resource

import (
	tls "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"google.golang.org/protobuf/proto"
)

func GetSecret(name string, resource proto.Message) *tls.Secret {
	secret := &tls.Secret{}
	secret.Name = name

	switch v := resource.(type) {
	case *tls.TlsCertificate:
		secret.Type = &tls.Secret_TlsCertificate{TlsCertificate: v}
	case *tls.CertificateValidationContext:
		secret.Type = &tls.Secret_ValidationContext{ValidationContext: v}
	case *tls.TlsSessionTicketKeys:
		secret.Type = &tls.Secret_SessionTicketKeys{SessionTicketKeys: v}
	case *tls.GenericSecret:
		secret.Type = &tls.Secret_GenericSecret{GenericSecret: v}
	default:
		return nil
	}

	return secret
}
